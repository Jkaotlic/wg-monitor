# Command-result → TG reply trace

Pre-T15/T16 snapshot of the command-result path. Verified against worktree
HEAD `26e4cc9` on `feature/stage-2`. Companion to `docs/superpowers/plans/
2026-04-30-ui-replykeyboard-hybrid.md` Phase E.

## Current path (DEAD END at step 7)

1. TG admin taps inline button → router receives `CallbackQuery`
2. `callbacks/router.go::HandleCallback` dispatches to `CommandAction.Apply`
3. `callbacks/actions.go:212-226` — `CommandAction.Apply` builds
   `wire.Command{ID, Action, Args, IssuedAt}` and calls
   `a.sink.Enqueue(args.UserID, cmd)` — **bare `Enqueue`, no MessageRef**
4. Agent long-polls `GET /v1/cmd` → `internal/backend/handler.go:141-177`
   `cmdGetHandler` → `CommandSink.Dequeue` → JSON body returned
5. Agent runs the action via `agent/actions/runner.go`, then
   `POST /v1/cmd/result` with `wire.CommandResult{ID, Status, Output, ...}`
6. `internal/backend/handler.go:179-217` `cmdResultHandler` parses + validates,
   calls `CommandSink.RecordResult(uid, res)` (`cmd/queue.go:93-110`)
7. **DEAD END.** Handler logs `cmd result …` and writes 200. There is no
   subscriber that relays `res` back to TG; `AwaitResult` exists
   (`cmd/queue.go:115-142`) but no callsite invokes it from the result-handler
   path.

## What needs to change

### T15 — `cmd.Queue` carries `MessageRef`

- New value type `MessageRef{ChatID, MessageID, ThreadID, Action}` in
  `internal/backend/cmd/queue.go`.
- New parallel map `origins map[int64]map[string]MessageRef` on `Queue`
  (userID → cmd.ID → ref). Initialised in `New()`.
- New method `EnqueueWithRef(userID, cmd, ref)` — calls existing `Enqueue`,
  then stores `ref` (with `ref.Action` populated from `cmd.Action`).
- New method `OriginRef(userID, cmdID) (MessageRef, bool)` — read.
- New method `ConsumeOriginRef(userID, cmdID) (MessageRef, bool)` — read +
  delete in one shot, defends against double-relay if `RecordResult` ever
  fires twice for the same id.

Bare `Enqueue` stays untouched — it is the no-ref path used by paths that do
not originate from a TG button (none today, but keeping the surface keeps the
test-fakes simple).

### T16 — handler relays to TG

- `internal/backend/handler.go::CommandSink` interface gains
  `ConsumeOriginRef(userID, cmdID) (cmd.MessageRef, bool)` — `*cmd.Queue`
  satisfies it after T15.
- `internal/backend/handler.go::Deps` gains `TGNotifier TGNotifier` and
  `UI UIConfig` fields.
- New `TGNotifier` interface in `handler.go`:

  ```go
  type TGNotifier interface {
      NotifyCommandResult(ctx context.Context, ref cmd.MessageRef,
          action string, result wire.CommandResult, maxChars int) error
  }
  ```

- `cmdResultHandler` (handler.go:179-217), after the existing `RecordResult`
  call: `ConsumeOriginRef`; if found, fire-and-forget goroutine that calls
  `d.TGNotifier.NotifyCommandResult(ctx30s, ref, ref.Action, res, maxChars)`.
  Failures log a warn — must not 500 the agent's result POST.
- Concrete `callbacks.Notifier` in new file
  `internal/backend/callbacks/notifier.go`:
  - `NotifyCommandResult` builds `chunks := alerts.FormatCommandResult(...)`
  - Sends each chunk via `tg.Client.SendMessageWithReplyKeyboard`, first reply
    is to `ref.MessageID`, subsequent replies chain to the previous chunk's
    returned message-id
  - Carries `ReplyKeyboardForTopic("per_router")` so the persistent
    ReplyKeyboard remains attached after the result lands.

### `CommandAction.Apply` — switch to `EnqueueWithRef` when callback present

`actions.go:212-226` becomes branched: if `q != nil`, build `MessageRef` from
`q.Message.{Chat.ID, MessageID, MessageThreadID}` and call `EnqueueWithRef`;
else fall back to `Enqueue` (covers tests that bypass TG).

`CommandEnqueuer` interface in `actions.go:19-21` extends with the new method.

## Wiring (deferred to T24 deploy)

`cmd/backend/main.go` needs to construct `&callbacks.Notifier{TG, Cfg}` and
pass it as `Deps.TGNotifier`. Plan defers this to Task 24 (deploy phase) so
T16 stays test-only and can land on its own commit.

## Why this shape

- **Origins map separate from `pending`**: `Dequeue` consumes from `pending`,
  but the result lands minutes (or never) later. Decoupling lets `Dequeue`
  stay unchanged and keeps the relay path additive.
- **`ConsumeOriginRef` deletes on read**: backend restart drops the queue, so
  the only way the same ref gets relayed twice is if `RecordResult` fires
  twice (unlikely, but free defence).
- **Async relay**: the agent's `POST /v1/cmd/result` must not stall on TG
  network latency. Fire-and-forget goroutine with 30s context cap keeps the
  agent path snappy.
- **First chunk replies to original**: `ref.MessageID` is the alert that
  triggered the action. Subsequent chunks reply to the previous chunk to keep
  multi-page diag output threaded together rather than scattered in the topic.
