package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

type SecretSource int

const (
	SourceMissing SecretSource = iota
	SourceEnv
	SourceMemoryFile
	SourcePrompt
)

func (s SecretSource) String() string {
	switch s {
	case SourceEnv:
		return "env"
	case SourceMemoryFile:
		return "memory file"
	case SourcePrompt:
		return "prompt"
	}
	return "missing"
}

type MemoryFileLookup struct {
	Path    string
	Pattern string // regexp with one capture group
}

type SecretStore struct {
	prompted map[string]bool
}

func NewSecretStore() *SecretStore {
	return &SecretStore{prompted: map[string]bool{}}
}

// Get tries: env var → memory file (if provided) → prompt.
// Returns (secret, source). Empty string and SourceMissing if even prompt failed.
func (s *SecretStore) Get(envVar, label string, mem *MemoryFileLookup) (string, SecretSource) {
	if v := os.Getenv(envVar); v != "" {
		return v, SourceEnv
	}
	if mem != nil {
		if v, ok := lookupMemoryFile(mem.Path, mem.Pattern); ok {
			return v, SourceMemoryFile
		}
	}
	v := AskSecret(label)
	if v == "" {
		return "", SourceMissing
	}
	s.recordPrompted(envVar)
	return v, SourcePrompt
}

func (s *SecretStore) recordPrompted(envVar string) {
	s.prompted[envVar] = true
}

// PromptedSecrets returns the list of env-var names that were prompted in this session.
// Used to show a warning at the end advising the user to save them.
func (s *SecretStore) PromptedSecrets() []string {
	out := make([]string, 0, len(s.prompted))
	for k := range s.prompted {
		out = append(out, k)
	}
	return out
}

func lookupMemoryFile(path, pattern string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}
	m := re.FindStringSubmatch(string(data))
	if len(m) < 2 {
		return "", false
	}
	return strings.TrimSpace(m[1]), true
}

// PrintSecretsSaveAdvice prints instructions to set the prompted secrets as env vars,
// so the user doesn't re-enter them next run.
func PrintSecretsSaveAdvice(store *SecretStore) {
	prompted := store.PromptedSecrets()
	if len(prompted) == 0 {
		return
	}
	PrintWarn("В этой сессии ты ввёл секреты вручную: " + strings.Join(prompted, ", "))
	fmt.Println("  Чтобы не вводить заново, сохрани их в env vars:")
	fmt.Println()
	fmt.Println("  PowerShell (постоянно):")
	for _, name := range prompted {
		fmt.Printf("    [Environment]::SetEnvironmentVariable(\"%s\", \"<значение>\", \"User\")\n", name)
	}
	fmt.Println()
	fmt.Println("  Bash/Zsh (~/.zshrc или ~/.bashrc):")
	for _, name := range prompted {
		fmt.Printf("    export %s=\"<значение>\"\n", name)
	}
	fmt.Println()
}
