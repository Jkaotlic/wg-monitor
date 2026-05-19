package main

var activeProfile string

func setActiveProfile(profile string) {
	activeProfile = sanitizeProfileName(profile)
}

func currentProfile() string {
	if activeProfile == "" {
		return "default"
	}
	return activeProfile
}
