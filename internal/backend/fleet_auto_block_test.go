package backend

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
)

// REV-05: строка «Парк» говорит, почему авто-оживление не пойдёт, не только
// про пароль и адрес: пароль не расшифровывается, админ снял оживление,
// ждём сутки после прошлого итога.
func TestMiniappFleetShowsWhyAutoReviveWaits(t *testing.T) {
	setup := func(t *testing.T) *adminOpsEnv {
		env := newAdminOpsEnv(t, withRealRevive(t))
		if _, err := env.d.SQL().Exec(`UPDATE users SET last_deployed_version = 'v0.12.0', awgm_url = 'https://router.example.com' WHERE id = ?`, env.ownedID); err != nil {
			t.Fatal(err)
		}
		return env
	}
	blocked := func(t *testing.T, env *adminOpsEnv) string {
		return fleetRowOf(t, fleetResponse(t, fleetRequest(t, env.h, 999)), env.ownedID).AutoReviveBlocked
	}

	t.Run("unreadable", func(t *testing.T) {
		env := setup(t)
		other, _ := revive.NewBox(bytes.Repeat([]byte{7}, revive.KeySize))
		nonce, ct, _ := other.Seal(env.ownedID, revive.NewSecrets(miniappReviveRoot, "", "", ""))
		if err := env.d.RouterCredentials().Put(env.ownedID, nonce, ct, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := blocked(t, env); !strings.Contains(got, "не расшифровывается") {
			t.Fatalf("blocked=%q", got)
		}
	})
	t.Run("cancelled by admin", func(t *testing.T) {
		env := setup(t)
		saveFixtureCreds(t, env.d, env.ownedID)
		time.Sleep(10 * time.Millisecond) // отмена строго позже сохранения
		if out, err := env.revive.AutoSchedule(context.Background(), env.ownedID); err != nil || out != revive.AutoScheduled {
			t.Fatalf("%v %v", out, err)
		}
		env.revive.Wait()
		if ok, err := env.revive.Cancel(context.Background(), env.ownedID); err != nil || !ok {
			t.Fatalf("cancel: %v %v", ok, err)
		}
		if got := blocked(t, env); !strings.Contains(got, "снял") {
			t.Fatalf("blocked=%q", got)
		}
	})
	t.Run("cooldown", func(t *testing.T) {
		env := setup(t)
		saveFixtureCreds(t, env.d, env.ownedID)
		if _, err := env.revive.AutoSchedule(context.Background(), env.ownedID); err != nil {
			t.Fatal(err)
		}
		env.revive.Wait()
		in, _ := env.d.Revive().Get(env.ownedID)
		if ok, err := env.d.Revive().Finish(env.ownedID, []string{revive.StatusWaiting}, revive.StatusFailed, "переустановка не удалась", time.Now(), in.Generation); err != nil || !ok {
			t.Fatalf("finish: %v %v", ok, err)
		}
		if got := blocked(t, env); !strings.Contains(got, "через") {
			t.Fatalf("blocked=%q", got)
		}
	})
}
