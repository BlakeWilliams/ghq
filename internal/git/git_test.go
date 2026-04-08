package git

import (
	"testing"
)

func TestParseDiffToFiles(t *testing.T) {
	rawDiff := `diff --git a/cmd/ghq/main.go b/cmd/ghq/main.go
index 12aca0d..468cdfa 100644
--- a/cmd/ghq/main.go
+++ b/cmd/ghq/main.go
@@ -28,7 +28,7 @@ func main() {
 		GCInterval: 1 * time.Minute,
 	})

-	p := tea.NewProgram(ui.NewApp(cachedClient))
+	p := tea.NewProgram(ui.NewApp(cachedClient, *nwo))
 	if _, err := p.Run(); err != nil {
 		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
 		os.Exit(1)
diff --git a/internal/github/cached.go b/internal/github/cached.go
index d91fa87..c3f7922 100644
--- a/internal/github/cached.go
+++ b/internal/github/cached.go
@@ -42,6 +42,12 @@ func NewCachedClient(client *Client, opts cache.Options) *CachedClient {
 	}
 }

+// SetRepo changes the active repo scope for API calls.
+func (c *CachedClient) SetRepo(owner, repo string) {
+	c.client.owner = owner
+	c.client.repo = repo
+}
+
 // RepoFullName returns the owner/repo string.
 func (c *CachedClient) RepoFullName() string {
 	return c.client.RepoFullName()
`

	files := ParseDiffToFiles(rawDiff)

	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	// First file.
	f := files[0]
	if f.Filename != "cmd/ghq/main.go" {
		t.Errorf("expected filename cmd/ghq/main.go, got %s", f.Filename)
	}
	if f.Status != "modified" {
		t.Errorf("expected status modified, got %s", f.Status)
	}
	if f.Additions != 1 {
		t.Errorf("expected 1 addition, got %d", f.Additions)
	}
	if f.Deletions != 1 {
		t.Errorf("expected 1 deletion, got %d", f.Deletions)
	}
	// Patch should start with @@, not diff --git or ---/+++.
	if len(f.Patch) == 0 {
		t.Fatal("patch is empty")
	}
	if f.Patch[0] != '@' {
		t.Errorf("patch should start with @@, starts with: %q", f.Patch[:20])
	}

	// Second file.
	f2 := files[1]
	if f2.Filename != "internal/github/cached.go" {
		t.Errorf("expected filename internal/github/cached.go, got %s", f2.Filename)
	}
	if f2.Additions != 6 {
		t.Errorf("expected 6 additions, got %d", f2.Additions)
	}
	if f2.Deletions != 0 {
		t.Errorf("expected 0 deletions, got %d", f2.Deletions)
	}
}

func TestParseDiffToFiles_NewFile(t *testing.T) {
	rawDiff := `diff --git a/newfile.go b/newfile.go
new file mode 100644
index 0000000..1234567
--- /dev/null
+++ b/newfile.go
@@ -0,0 +1,3 @@
+package main
+
+func hello() {}
`

	files := ParseDiffToFiles(rawDiff)

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Status != "added" {
		t.Errorf("expected status added, got %s", files[0].Status)
	}
	if files[0].Additions != 3 {
		t.Errorf("expected 3 additions, got %d", files[0].Additions)
	}
}

func TestParseDiffToFiles_DeletedFile(t *testing.T) {
	rawDiff := `diff --git a/old.go b/old.go
deleted file mode 100644
index 1234567..0000000
--- a/old.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package old
-func bye() {}
`

	files := ParseDiffToFiles(rawDiff)

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Status != "removed" {
		t.Errorf("expected status removed, got %s", files[0].Status)
	}
	if files[0].Deletions != 2 {
		t.Errorf("expected 2 deletions, got %d", files[0].Deletions)
	}
}

func TestParseDiffToFiles_RenamedFile(t *testing.T) {
	rawDiff := `diff --git a/old_name.go b/new_name.go
similarity index 95%
rename from old_name.go
rename to new_name.go
index 1234567..abcdefg 100644
--- a/old_name.go
+++ b/new_name.go
@@ -1,3 +1,3 @@
 package main

-func oldFunc() {}
+func newFunc() {}
`

	files := ParseDiffToFiles(rawDiff)

	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Status != "renamed" {
		t.Errorf("expected status renamed, got %s", files[0].Status)
	}
	if files[0].Filename != "new_name.go" {
		t.Errorf("expected new filename, got %s", files[0].Filename)
	}
	if files[0].PreviousFilename != "old_name.go" {
		t.Errorf("expected previous filename old_name.go, got %s", files[0].PreviousFilename)
	}
}

func TestParseDiffToFiles_EmptyDiff(t *testing.T) {
	files := ParseDiffToFiles("")
	if files != nil {
		t.Errorf("expected nil for empty diff, got %v", files)
	}
}
