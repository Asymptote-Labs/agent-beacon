package asymptoteobserve

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestShareRemoteURLStripsPrivateSuffixWithoutPath(t *testing.T) {
	cases := map[string]string{
		"https://git.example.test?access_token=private-query":                    "https://git.example.test",
		"https://git.example.test#private-fragment":                              "https://git.example.test",
		"https://git.example.test?access_token=private-query#private-fragment":   "https://git.example.test",
		"https://git.example.test?redirect=/private-query":                       "https://git.example.test",
		"https://git.example.test?login=private-query@other.example":             "https://git.example.test",
		"https://user:pw@git.example.test?redirect=/private-query@other.example": "https://git.example.test",
		"ssh://git@[2001:db8::1]:2222?token=private-query":                       "ssh://[2001:db8::1]:2222",
		"HTTPS://git.example.test:8443#private-fragment":                         "HTTPS://git.example.test:8443",
		"https://?access_token=private-query":                                    "",
		"https://#private-fragment":                                              "",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := ShareRemoteURL(input); got != want {
				t.Fatalf("ShareRemoteURL(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestShareRemoteURLKeepsPublicRemoteForms(t *testing.T) {
	cases := map[string]string{
		"https://git.example.test/repo.git?token=private-query#private-fragment": "https://git.example.test/repo.git",
		"https://user:pw@git.example.test/repo.git":                              "https://git.example.test/repo.git",
		"https://git.example.test":                                               "https://git.example.test",
		"  https://git.example.test:8443  ":                                      "https://git.example.test:8443",
		"ssh://git@[2001:db8::1]:2222/repo.git":                                  "ssh://[2001:db8::1]:2222/repo.git",
		"https://git.example.test/repo%3Fname%23part.git":                        "https://git.example.test/repo%3Fname%23part.git",
		"git@host.example:repo.git":                                              "host.example:repo.git",
		"host.example:repo.git":                                                  "host.example:repo.git",
		"git@host.example:repo?revision#part":                                    "host.example:repo?revision#part",
		"":                                                                       "",
		"https://":                                                               "",
		"https:///repo.git":                                                      "",
		"/home/alice/repo":                                                       "",
		"../repo.git":                                                            "",
		"~/repo.git":                                                             "",
		"file:///home/alice/repo":                                                "",
		"FILE://host.example/repo":                                               "",
		`C:\Users\alice\repo`:                                                    "",
		"C:/Users/alice/repo":                                                    "",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			if got := ShareRemoteURL(input); got != want {
				t.Fatalf("ShareRemoteURL(%q) = %q, want %q", input, got, want)
			}
		})
	}
}

func TestShareTraceBodyOmitsPrivateRemoteSuffix(t *testing.T) {
	for _, metadataOnly := range []bool{false, true} {
		mode := "full"
		if metadataOnly {
			mode = "metadata"
		}
		t.Run(mode, func(t *testing.T) {
			for _, remote := range []string{
				"https://git.example.test?access_token=private-query",
				"https://git.example.test#private-fragment",
				"https://git.example.test?redirect=/private-query@other.example#private-fragment",
			} {
				t.Run(remote, func(t *testing.T) {
					event := Event{
						Timestamp: "2026-10-22T10:00:00Z",
						Event:     EventInfo{ID: "shared-remote-prompt", Kind: "agent_runtime", Action: "prompt.submitted", Category: "prompt"},
						Harness:   HarnessInfo{Name: "claude_code"},
						Session:   &SessionInfo{ID: "shared-remote-session"},
						Prompt:    &PromptInfo{Text: "Check the repository"},
						VCS:       &VCSInfo{Repository: &VCSRepositoryInfo{URL: &VCSRepositoryURLInfo{Full: remote}}},
					}
					original, err := json.Marshal(event)
					if err != nil {
						t.Fatal(err)
					}
					bundle := BuildShareBundle([]Event{event}, ShareOptions{NoHomeLookup: true, MetadataOnly: metadataOnly, GeneratedAt: time.Unix(0, 0)})
					body, err := json.Marshal(NewShareTrace(bundle, ShareVisibilityDirect))
					if err != nil {
						t.Fatal(err)
					}
					if bundle.Summary.Repository == nil || bundle.Summary.Repository.RemoteURL != "https://git.example.test" {
						t.Errorf("repository = %+v, want root remote without private suffix", bundle.Summary.Repository)
					}
					if bundle.Summary.Project == nil || bundle.Summary.Project.Name != "git.example.test" {
						t.Errorf("project = %+v, want public host name", bundle.Summary.Project)
					}
					for _, private := range []string{"private-query", "private-fragment", "other.example"} {
						if strings.Contains(string(body), private) {
							t.Errorf("share body retains %q: %s", private, body)
						}
					}
					if len(bundle.Events) != 1 || bundle.Events[0].ID != event.Event.ID {
						t.Errorf("event identity changed: %+v", bundle.Events)
					}
					after, err := json.Marshal(event)
					if err != nil {
						t.Fatal(err)
					}
					if string(after) != string(original) {
						t.Fatal("BuildShareBundle mutated the caller event")
					}
				})
			}
		})
	}
}
