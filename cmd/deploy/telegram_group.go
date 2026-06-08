package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func actionTelegramGroup(state *State, secrets *SecretStore, nickname string) error {
	if state.Backend.Host == "" {
		return fmt.Errorf("backend is not configured; run install-backend first")
	}
	ag, err := chooseAgentForAction(state, nickname, "Telegram group/topic binding")
	if err != nil {
		return err
	}
	if !cliNicknameRe.MatchString(ag.Nickname) {
		return fmt.Errorf("invalid nickname %q", ag.Nickname)
	}

	defaultChat := ""
	if len(state.Telegram.ExtraChatIDs) > 0 {
		defaultChat = fmt.Sprint(state.Telegram.ExtraChatIDs[len(state.Telegram.ExtraChatIDs)-1])
	}
	chatID := parseInt64Or(Ask("Telegram supergroup chat_id", defaultChat), 0)
	if chatID == 0 {
		return fmt.Errorf("telegram chat_id required")
	}
	threadID := parseIntOr(Ask("thread_id (0 = create topic automatically)", "0"), 0)

	var added bool
	state.Telegram.ExtraChatIDs, added = appendExtraChatID(state.Telegram.ExtraChatIDs, state.Telegram.ChatID, chatID)
	if added {
		PrintInfo(fmt.Sprintf("wizard.toml: added extra_chat_ids entry %d", chatID))
	}

	kh, err := NewKnownHosts(defaultCacheDir() + "/known_hosts")
	if err != nil {
		return err
	}
	PrintStep(1, 4, "SSH to backend")
	s, err := connectBackendSSH(state, secrets, kh)
	if err != nil {
		return err
	}
	defer s.Close()

	layout := backupLayoutForState(state)
	cliPath, err := detectBackendCLIPath(s, state)
	if err != nil {
		return err
	}

	if chatID != state.Telegram.ChatID {
		PrintStep(2, 4, "Allow Telegram group in backend.yaml")
		if err := applyTelegramExtraChatIDToBackend(s, state, layout, chatID); err != nil {
			return err
		}
	} else {
		PrintStep(2, 4, "Allow Telegram group in backend.yaml")
		PrintSkip("chat_id is the primary Telegram group; extra_chat_ids unchanged")
	}

	PrintStep(3, 4, "Bind router to Telegram chat")
	if err := runBackendBindTopic(s, cliPath, backendDBPathForState(state), ag.Nickname, chatID, threadID); err != nil {
		return err
	}

	PrintStep(4, 4, "Ensure topic")
	if threadID > 0 {
		ag.ThreadID = threadID
		PrintOK(fmt.Sprintf("%s bound to chat_id=%d thread_id=%d", ag.Nickname, chatID, threadID))
		return nil
	}
	createdThreadID, err := runBackendEnsureTopic(s, cliPath, layout.ConfigPath, backendDBPathForState(state), ag.Nickname)
	if err != nil {
		return fmt.Errorf("ensure topic: %w", err)
	}
	if createdThreadID > 0 {
		ag.ThreadID = createdThreadID
	}
	PrintOK(fmt.Sprintf("%s bound to chat_id=%d; topic ensured by backend CLI", ag.Nickname, chatID))
	return nil
}

func applyTelegramExtraChatIDToBackend(s *SSH, state *State, layout backupRemoteLayout, chatID int64) error {
	raw, ok := readRemoteFile(s, layout.ConfigPath)
	if !ok {
		return fmt.Errorf("read %s: failed", layout.ConfigPath)
	}
	updated, changed, err := ensureYAMLTelegramExtraChatID([]byte(raw), chatID)
	if err != nil {
		return fmt.Errorf("patch %s: %w", layout.ConfigPath, err)
	}
	if !changed {
		PrintSkip("backend.yaml already allows this chat_id")
		return nil
	}
	stamp := time.Now().UTC().Format("20060102150405")
	backup := layout.ConfigPath + ".bak-telegram-" + stamp
	if _, err := s.MustRun("cp -p " + shellSingleQuote(layout.ConfigPath) + " " + shellSingleQuote(backup)); err != nil {
		return fmt.Errorf("backup backend.yaml: %w", err)
	}
	if err := stepUploadFile(s, layout.ConfigPath, updated, "640"); err != nil {
		return err
	}
	if _, err := s.MustRun("chown " + shellSingleQuote(backendConfigOwner(state, layout)) + " " + shellSingleQuote(layout.ConfigPath)); err != nil {
		PrintWarn("chown backend.yaml: " + err.Error())
	}
	if _, err := s.MustRun(backendRestartCommandForState(state)); err != nil {
		return fmt.Errorf("restart backend: %w", err)
	}
	if state.Backend.Domain != "" {
		if err := stepVerifyBackendHealth(s, state.Backend.Domain); err != nil {
			return err
		}
	}
	return nil
}

func runBackendBindTopic(s *SSH, cliPath, dbPath, nickname string, chatID int64, threadID int) error {
	args := []string{
		cliPath,
		"bind-topic",
		"--nickname=" + nickname,
		fmt.Sprintf("--chat-id=%d", chatID),
		"--db=" + dbPath,
	}
	if threadID > 0 {
		args = append(args, fmt.Sprintf("--thread-id=%d", threadID))
	}
	out, err := s.MustRun(shellCommand(args...))
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "" {
		PrintInfo(strings.TrimSpace(out))
	}
	return nil
}

func runBackendEnsureTopic(s *SSH, cliPath, configPath, dbPath, nickname string) (int, error) {
	args := []string{
		cliPath,
		"ensure-topics",
		"--config=" + configPath,
		"--nickname=" + nickname,
		"--db=" + dbPath,
		"--sleep-ms=0",
	}
	out, err := s.MustRun(shellCommand(args...))
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(out) != "" {
		PrintInfo(strings.TrimSpace(out))
	}
	return extractThreadIDFromEnsureTopicsOutput(out), nil
}

var ensureTopicsThreadIDRe = regexp.MustCompile(`topic id=(\d+)`)

func extractThreadIDFromEnsureTopicsOutput(out string) int {
	m := ensureTopicsThreadIDRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return 0
	}
	id, _ := strconv.Atoi(m[1])
	return id
}

func detectBackendCLIPath(s *SSH, state *State) (string, error) {
	candidates := []string{"/usr/local/bin/wg-monitor-cli"}
	if strings.TrimSpace(state.Backend.SourceBind) != "" {
		candidates = append([]string{backendDockerBase(userOrDefault(state.Backend.User, "root")) + "/bin/wg-monitor-cli"}, candidates...)
	}
	for _, p := range candidates {
		if _, _, rc, _ := s.Run("test -x " + shellSingleQuote(p)); rc == 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("wg-monitor-cli not found on backend; update backend components first")
}

func backendDBPathForState(state *State) string {
	if state != nil && strings.TrimSpace(state.Backend.SourceBind) != "" {
		return backendDockerBase(userOrDefault(state.Backend.User, "root")) + "/data/state.db"
	}
	return "/var/lib/wg-monitor/state.db"
}

func backendConfigOwner(state *State, layout backupRemoteLayout) string {
	if state != nil && strings.TrimSpace(state.Backend.SourceBind) != "" {
		return layout.User + ":" + layout.Group
	}
	return "root:wgmonitor"
}

func backendRestartCommandForState(state *State) string {
	if state != nil && strings.TrimSpace(state.Backend.SourceBind) != "" {
		return "docker restart wg-monitor-backend >/dev/null"
	}
	return "systemctl restart wg-monitor-backend"
}

func shellCommand(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellSingleQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func normalizeExtraChatIDs(ids []int64, primary int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if id == 0 || id == primary || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func appendExtraChatID(ids []int64, primary, id int64) ([]int64, bool) {
	normalized := normalizeExtraChatIDs(ids, primary)
	if id == 0 || id == primary {
		return normalized, false
	}
	for _, existing := range normalized {
		if existing == id {
			return normalized, false
		}
	}
	return append(normalized, id), true
}

func ensureYAMLTelegramExtraChatID(raw []byte, chatID int64) ([]byte, bool, error) {
	if chatID == 0 {
		return nil, false, fmt.Errorf("chat_id must be non-zero")
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false, err
	}
	root := yamlDocumentMapping(&doc)
	if root == nil {
		return nil, false, fmt.Errorf("backend.yaml root must be a mapping")
	}
	telegram := yamlMappingChild(root, "telegram")
	if telegram == nil {
		return nil, false, fmt.Errorf("backend.yaml missing telegram block")
	}
	if telegram.Kind != yaml.MappingNode {
		return nil, false, fmt.Errorf("telegram block must be a mapping")
	}
	extra := yamlMappingChild(telegram, "extra_chat_ids")
	if extra == nil {
		extra = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		yamlSetMappingChild(telegram, "extra_chat_ids", extra)
	} else if extra.Kind != yaml.SequenceNode {
		return nil, false, fmt.Errorf("telegram.extra_chat_ids must be a list")
	}
	extra.Style = 0
	for _, item := range extra.Content {
		if strings.TrimSpace(item.Value) == strconv.FormatInt(chatID, 10) {
			return raw, false, nil
		}
	}
	extra.Content = append(extra.Content, &yaml.Node{
		Kind:  yaml.ScalarNode,
		Tag:   "!!int",
		Value: strconv.FormatInt(chatID, 10),
	})
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

func yamlDocumentMapping(doc *yaml.Node) *yaml.Node {
	if doc == nil {
		return nil
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		return doc.Content[0]
	}
	if doc.Kind == yaml.MappingNode {
		return doc
	}
	return nil
}

func yamlMappingChild(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func yamlSetMappingChild(mapping *yaml.Node, key string, value *yaml.Node) {
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}
