package panel

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveThunderstoreSearch(t *testing.T) {
	if os.Getenv("THUNDERSTORE_LIVE") != "1" {
		t.Skip("set THUNDERSTORE_LIVE=1 to run live Thunderstore test")
	}
	client := NewThunderstore("valheim", false, "")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := client.Search(ctx, "Jotunn", 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) == 0 {
		t.Fatal("no packages returned")
	}
	pkg, err := client.FindPackage(ctx, "valheimmodding-jotunn")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Version == "" || pkg.DownloadURL == "" || pkg.FullName == "" {
		t.Fatalf("incomplete package: %+v", pkg)
	}
	queue, err := client.ResolveWithDependencies(ctx, pkg)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue) < 2 {
		t.Fatalf("dependency resolution returned %d packages: %+v", len(queue), queue)
	}
}
