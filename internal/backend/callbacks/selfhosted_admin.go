package callbacks

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func (r *Router) adminSelfHostedAmnezia(ctx context.Context, m *tg.Message, arg string) {
	fields := strings.Fields(strings.TrimSpace(arg))
	if len(fields) == 0 {
		r.adminReply(ctx, m, r.selfHostedAmneziaListText())
		return
	}
	switch fields[0] {
	case "add":
		r.adminSelfHostedAdd(ctx, m, fields[1:])
	case "set":
		r.adminSelfHostedSet(ctx, m, fields[1:])
	case "delete", "del", "rm":
		r.adminSelfHostedDelete(ctx, m, fields[1:])
	case "enable":
		r.adminSelfHostedEnable(ctx, m, fields[1:], true)
	case "disable":
		r.adminSelfHostedEnable(ctx, m, fields[1:], false)
	default:
		r.adminReply(ctx, m, selfHostedUsage())
	}
}

func (r *Router) selfHostedAmneziaListText() string {
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		return "Self-hosted Amnezia: не удалось прочитать storage: " + err.Error()
	}
	if len(store.Instances) == 0 {
		return selfHostedUsage()
	}
	var b strings.Builder
	b.WriteString("Self-hosted Amnezia VPS:\n\n")
	for _, inst := range store.Instances {
		state := "off"
		if inst.Enabled {
			state = "on"
		}
		fmt.Fprintf(&b, "  • %s [%s] %s:%d", inst.ID, state, inst.EndpointHost, inst.EndpointPort)
		if strings.TrimSpace(inst.Label) != "" && inst.Label != inst.ID {
			fmt.Fprintf(&b, " — %s", inst.Label)
		}
		b.WriteByte('\n')
	}
	b.WriteString("\n")
	b.WriteString(selfHostedUsage())
	return b.String()
}

func (r *Router) adminSelfHostedAdd(ctx context.Context, m *tg.Message, fields []string) {
	if len(fields) < 3 {
		r.adminReply(ctx, m, "Usage: /selfhosted add <id> <endpoint_host> <endpoint_port> [label]")
		return
	}
	port, err := strconv.Atoi(fields[2])
	if err != nil {
		r.adminReply(ctx, m, "endpoint_port must be a number")
		return
	}
	inst := selfhostedamnezia.Instance{
		ID:           fields[0],
		EndpointHost: fields[1],
		EndpointPort: port,
		Label:        strings.Join(fields[3:], " "),
	}
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		r.adminReply(ctx, m, "Failed to read self-hosted storage: "+err.Error())
		return
	}
	if err := store.Upsert(inst); err != nil {
		r.adminReply(ctx, m, "Failed to save self-hosted VPS: "+err.Error())
		return
	}
	if err := r.saveSelfHostedAmneziaStore(store); err != nil {
		r.adminReply(ctx, m, "Failed to write self-hosted storage: "+err.Error())
		return
	}
	r.adminReply(ctx, m, fmt.Sprintf("Self-hosted Amnezia VPS %q saved.", strings.ToLower(fields[0])))
}

func (r *Router) adminSelfHostedSet(ctx context.Context, m *tg.Message, fields []string) {
	if len(fields) < 2 {
		r.adminReply(ctx, m, "Usage: /selfhosted set <id> key=value [key=value...]")
		return
	}
	id := strings.ToLower(fields[0])
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		r.adminReply(ctx, m, "Failed to read self-hosted storage: "+err.Error())
		return
	}
	inst, ok := store.Get(id)
	if !ok {
		r.adminReply(ctx, m, "Self-hosted VPS not found: "+id)
		return
	}
	for _, raw := range fields[1:] {
		key, val, ok := strings.Cut(raw, "=")
		if !ok {
			r.adminReply(ctx, m, "Expected key=value, got: "+raw)
			return
		}
		if err := applySelfHostedField(&inst, strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)); err != nil {
			r.adminReply(ctx, m, err.Error())
			return
		}
	}
	if err := store.Upsert(inst); err != nil {
		r.adminReply(ctx, m, "Failed to update self-hosted VPS: "+err.Error())
		return
	}
	if err := r.saveSelfHostedAmneziaStore(store); err != nil {
		r.adminReply(ctx, m, "Failed to write self-hosted storage: "+err.Error())
		return
	}
	r.adminReply(ctx, m, "Self-hosted Amnezia VPS "+id+" updated.")
}

func (r *Router) adminSelfHostedDelete(ctx context.Context, m *tg.Message, fields []string) {
	if len(fields) != 1 {
		r.adminReply(ctx, m, "Usage: /selfhosted delete <id>")
		return
	}
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		r.adminReply(ctx, m, "Failed to read self-hosted storage: "+err.Error())
		return
	}
	if !store.Delete(fields[0]) {
		r.adminReply(ctx, m, "Self-hosted VPS not found: "+fields[0])
		return
	}
	if err := r.saveSelfHostedAmneziaStore(store); err != nil {
		r.adminReply(ctx, m, "Failed to write self-hosted storage: "+err.Error())
		return
	}
	r.adminReply(ctx, m, "Self-hosted Amnezia VPS "+strings.ToLower(fields[0])+" deleted.")
}

func (r *Router) adminSelfHostedEnable(ctx context.Context, m *tg.Message, fields []string, enabled bool) {
	if len(fields) != 1 {
		r.adminReply(ctx, m, "Usage: /selfhosted enable <id> OR /selfhosted disable <id>")
		return
	}
	store, err := r.loadSelfHostedAmneziaStore()
	if err != nil {
		r.adminReply(ctx, m, "Failed to read self-hosted storage: "+err.Error())
		return
	}
	if !store.SetEnabled(fields[0], enabled) {
		r.adminReply(ctx, m, "Self-hosted VPS not found: "+fields[0])
		return
	}
	if err := r.saveSelfHostedAmneziaStore(store); err != nil {
		r.adminReply(ctx, m, "Failed to write self-hosted storage: "+err.Error())
		return
	}
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	r.adminReply(ctx, m, "Self-hosted Amnezia VPS "+strings.ToLower(fields[0])+" "+state+".")
}

func applySelfHostedField(inst *selfhostedamnezia.Instance, key, val string) error {
	switch key {
	case "label":
		inst.Label = val
	case "host", "endpoint_host":
		inst.EndpointHost = val
	case "port", "endpoint_port":
		port, err := strconv.Atoi(val)
		if err != nil {
			return fmt.Errorf("endpoint_port must be a number")
		}
		inst.EndpointPort = port
	case "container":
		inst.Container = val
	case "interface":
		inst.Interface = val
	case "config_path":
		inst.ConfigPath = val
	case "clients_path":
		inst.ClientsPath = val
	case "server_public_key_path":
		inst.ServerPubPath = val
	case "preshared_key_path":
		inst.PSKPath = val
	case "dns":
		if val == "" {
			inst.DNS = nil
			return nil
		}
		inst.DNS = strings.Split(val, ",")
	default:
		return fmt.Errorf("unknown field %q", key)
	}
	return nil
}

func selfHostedUsage() string {
	return strings.Join([]string{
		"Commands:",
		"  /selfhosted add <id> <endpoint_host> <endpoint_port> [label]",
		"  /selfhosted set <id> host=<host> port=<port> label=<label> dns=1.1.1.1,8.8.8.8",
		"  /selfhosted enable <id>",
		"  /selfhosted disable <id>",
		"  /selfhosted delete <id>",
	}, "\n")
}
