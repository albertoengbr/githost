package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"githost.dev/githost/internal/reposervice"
	"githost.dev/githost/internal/store"
)

// TestSearchVisibility ensures search results respect repository
// visibility: anonymous visitors only ever see public repositories and
// their issues.
func TestSearchVisibility(t *testing.T) {
	h := newHarness(t)
	alice := h.createUser("alice", "correct-password-1")
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("login failed: %d", code)
	}
	ctx := context.Background()
	priv, err := h.svc.Create(ctx, alice, "secret-proj", reposervice.CreateOptions{
		Visibility: store.VisibilityPrivate, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	pub, err := h.svc.Create(ctx, alice, "open-proj", reposervice.CreateOptions{
		Visibility: store.VisibilityPublic, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateIssue(ctx, pub.ID, alice.ID,
		"leaky faucet", "the sink drips all night"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.CreateIssue(ctx, priv.ID, alice.ID,
		"secret-crash", "the private build crashed"); err != nil {
		t.Fatal(err)
	}

	// Anonymous repos search: public hit only.
	h.resetSession()
	page := h.get("/search?q=proj")
	if !strings.Contains(page, "open-proj") {
		t.Fatal("public repo missing from anonymous search")
	}
	if strings.Contains(page, "secret-proj") {
		t.Fatal("private repo leaked into anonymous search")
	}

	// Anonymous issue search: the public issue matches, the private one must not.
	page = h.get("/search?q=drips&in=issues")
	if !strings.Contains(page, "leaky faucet") {
		t.Fatal("public issue missing from anonymous search")
	}
	page = h.get("/search?q=crashed&in=issues")
	if strings.Contains(page, "secret-crash") {
		t.Fatal("private issue leaked into anonymous search")
	}

	// The owner sees everything.
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("login failed: %d", code)
	}
	page = h.get("/search?q=proj")
	if !strings.Contains(page, "secret-proj") || !strings.Contains(page, "open-proj") {
		t.Fatal("owner search missing a repository")
	}
	page = h.get("/search?q=crashed&in=issues")
	if !strings.Contains(page, "secret-crash") {
		t.Fatal("owner issue search missed the private issue")
	}
}

// TestWebhookPagesAndCreate covers gating, validation and round trips.
func TestWebhookPagesAndCreate(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "correct-password-1")
	h.createUser("bob", "correct-password-2")
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("login failed: %d", code)
	}
	form := url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, h.get("/new")))
	form.Set("name", "hooked")
	form.Set("visibility", "public")
	form.Set("default_branch", "main")
	resp := h.postForm("/new", form)
	resp.Body.Close()

	page := h.get("/alice/hooked/settings/webhooks")
	if !strings.Contains(page, "Payload URL") {
		t.Fatalf("webhooks page missing the create form:\n%s", page)
	}

	// Non-HTTP scheme is rejected with a friendly message.
	bad := url.Values{}
	bad.Set("csrf", csrfTokenFromHTML(t, page))
	bad.Set("url", "ftp://not-http.example.com/hook")
	bad.Set("secret", "0123456789abcdef0123")
	bad["events"] = []string{"push"}
	r := h.postForm("/alice/hooked/settings/webhooks/create", bad)
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(body), "http(s)") {
		t.Fatalf("invalid url: status %d body=%s", r.StatusCode, body)
	}

	// Short secrets are rejected (the HMAC needs entropy).
	short := url.Values{}
	short.Set("csrf", csrfTokenFromHTML(t, page))
	short.Set("url", "https://example.invalid/hook")
	short.Set("secret", "abc")
	short["events"] = []string{"push"}
	r = h.postForm("/alice/hooked/settings/webhooks/create", short)
	body, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "16 characters") {
		t.Fatalf("short secret: status %d body=%s", r.StatusCode, body)
	}

	// A valid hook is accepted and listed.
	good := url.Values{}
	good.Set("csrf", csrfTokenFromHTML(t, page))
	good.Set("url", "https://example.invalid/hook")
	good.Set("secret", "0123456789abcdef0123")
	good["events"] = []string{"push", "issues"}
	r = h.postForm("/alice/hooked/settings/webhooks/create", good)
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("create webhook status = %d", r.StatusCode)
	}
	page = h.get("/alice/hooked/settings/webhooks")
	if !strings.Contains(page, "example.invalid/hook") || !strings.Contains(page, "push,issues") {
		t.Fatal("webhook not listed after creation")
	}

	// Pause, then delete.
	pause := url.Values{}
	pause.Set("csrf", csrfTokenFromHTML(t, page))
	pause.Set("hook_id", "1")
	pause.Set("active", "0")
	r = h.postForm("/alice/hooked/settings/webhooks/toggle", pause)
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("pause status = %d", r.StatusCode)
	}
	page = h.get("/alice/hooked/settings/webhooks")
	if !strings.Contains(page, "paused") {
		t.Fatal("paused state not shown")
	}
	del := url.Values{}
	del.Set("csrf", csrfTokenFromHTML(t, page))
	del.Set("hook_id", "1")
	r = h.postForm("/alice/hooked/settings/webhooks/delete", del)
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete status = %d", r.StatusCode)
	}
	if strings.Contains(h.get("/alice/hooked/settings/webhooks"), "example.invalid/hook") {
		t.Fatal("webhook still listed after delete")
	}

	// A logged-in non-manager gets 404 (never 403 — existence is not disclosed).
	h.resetSession()
	if code := h.login("bob", "correct-password-2"); code != http.StatusSeeOther {
		t.Fatalf("bob login: %d", code)
	}
	r = h.getRaw("/alice/hooked/settings/webhooks")
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("bob webhooks status = %d, want 404", r.StatusCode)
	}
}

// TestReleaseRoundTrip drafts a release from an existing tag and checks
// that the markdown notes render on the list page.
func TestReleaseRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "correct-password-1")
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("login failed: %d", code)
	}
	form := url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, h.get("/new")))
	form.Set("name", "released")
	form.Set("visibility", "public")
	form.Set("default_branch", "main")
	resp := h.postForm("/new", form)
	resp.Body.Close()
	h.seedRepoContent("alice", "released") // pushes tag v1.0

	if !strings.Contains(h.get("/alice/released/releases"), "No releases") {
		t.Fatal("releases page should start empty")
	}
	newPage := h.get("/alice/released/releases/new")
	if !strings.Contains(newPage, "v1.0") {
		t.Fatal("release form missing the seeded tag")
	}

	form = url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, newPage))
	form.Set("tag", "v1.0")
	form.Set("name", "First release")
	form.Set("notes", "## Highlights\n- initial cut")
	resp = h.postForm("/alice/released/releases/new", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("release create status = %d", resp.StatusCode)
	}

	page := h.get("/alice/released/releases")
	if !strings.Contains(page, "First release") ||
		!strings.Contains(page, `<h2 id="highlights">Highlights</h2>`) {
		t.Fatalf("release page missing rendered content:\n%s", page)
	}
	if !strings.Contains(h.get("/alice/released/tags"), "Draft a release") {
		t.Fatal("tags page missing the release entry point")
	}

	// A tag that does not exist is refused.
	form = url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, h.get("/alice/released/releases/new")))
	form.Set("tag", "v9.9")
	resp = h.postForm("/alice/released/releases/new", form)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bogus tag: status = %d, want 400", resp.StatusCode)
	}
}

// TestLabelsCRUD covers the label manager page and permission gating.
func TestLabelsCRUD(t *testing.T) {
	h := newHarness(t)
	h.createUser("alice", "correct-password-1")
	h.createUser("bob", "correct-password-2")
	if code := h.login("alice", "correct-password-1"); code != http.StatusSeeOther {
		t.Fatalf("login failed: %d", code)
	}
	form := url.Values{}
	form.Set("csrf", csrfTokenFromHTML(t, h.get("/new")))
	form.Set("name", "labeled")
	form.Set("visibility", "public")
	form.Set("default_branch", "main")
	resp := h.postForm("/new", form)
	resp.Body.Close()

	page := h.get("/alice/labeled/settings/labels")
	if !strings.Contains(page, "New label") {
		t.Fatal("labels page missing the create form")
	}

	// Bad colors are rejected.
	bad := url.Values{}
	bad.Set("csrf", csrfTokenFromHTML(t, page))
	bad.Set("name", "neon")
	bad.Set("color", "zzzzzz")
	r := h.postForm("/alice/labeled/settings/labels/create", bad)
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad color: status = %d, want 400", r.StatusCode)
	}

	good := url.Values{}
	good.Set("csrf", csrfTokenFromHTML(t, page))
	good.Set("name", "bug")
	good.Set("color", "e04b4b")
	r = h.postForm("/alice/labeled/settings/labels/create", good)
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("create label status = %d", r.StatusCode)
	}
	page = h.get("/alice/labeled/settings/labels")
	if !strings.Contains(page, "bug") || !strings.Contains(page, "#e04b4b") {
		t.Fatal("label not listed after creation")
	}

	del := url.Values{}
	del.Set("csrf", csrfTokenFromHTML(t, page))
	del.Set("label_id", "1")
	r = h.postForm("/alice/labeled/settings/labels/delete", del)
	r.Body.Close()
	if r.StatusCode != http.StatusSeeOther {
		t.Fatalf("delete label status = %d", r.StatusCode)
	}
	if strings.Contains(h.get("/alice/labeled/settings/labels"), ">bug<") {
		t.Fatal("label still listed after delete")
	}

	// Bob is not a manager: 404.
	h.resetSession()
	if code := h.login("bob", "correct-password-2"); code != http.StatusSeeOther {
		t.Fatalf("bob login: %d", code)
	}
	r = h.getRaw("/alice/labeled/settings/labels")
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("bob labels status = %d, want 404", r.StatusCode)
	}
}
