package tickets

import "testing"

func TestProjectsAddedInTheAppAreKeptOnceAndCanBeRemoved(t *testing.T) {
	store := openStore(t)
	ctx := t.Context()
	for _, repo := range []string{"owner/web", "owner/api", "Owner/API"} {
		if err := store.AddProject(ctx, repo, "owain"); err != nil {
			t.Fatal(err)
		}
	}
	projects, err := store.Projects(ctx)
	if err != nil || len(projects) != 2 || projects[0].Repo != "owner/api" || projects[1].Repo != "owner/web" || projects[0].AddedBy != "owain" || projects[0].AddedAt.IsZero() {
		t.Fatalf("projects = %+v, err = %v", projects, err)
	}
	if removed, err := store.RemoveProject(ctx, "OWNER/web"); err != nil || !removed {
		t.Fatalf("removed = %v, err = %v", removed, err)
	}
	if removed, err := store.RemoveProject(ctx, "owner/web"); err != nil || removed {
		t.Fatalf("removed twice = %v, err = %v", removed, err)
	}
	if projects, _ := store.Projects(ctx); len(projects) != 1 {
		t.Fatalf("projects = %+v", projects)
	}
}
