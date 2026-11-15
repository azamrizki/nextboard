package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/version"
)

func cmdUpdate(args []string) error {
	fs, _ := newFlags("update")
	repo := fs.String("repo", version.Repo, "GitHub repo owner/name")
	force := fs.Bool("force", false, "reinstall even if the version is the same, or older")
	tag := fs.String("version", "", "install this release, such as a beta (v0.4.0-beta.1), instead of the latest stable one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *repo == "" {
		return errors.New("GitHub repo not known; use --repo owner/nextboard")
	}
	client := update.NewClient(*repo, 2*time.Minute)
	ctx := context.Background()
	var rel *update.Release
	var err error
	if *tag != "" {
		rel, err = client.ByTag(ctx, "v"+strings.TrimPrefix(*tag, "v"))
	} else {
		rel, err = client.Latest(ctx)
	}
	if err != nil {
		return err
	}
	latest := strings.TrimPrefix(rel.TagName, "v")
	current := strings.TrimPrefix(version.Version, "v")
	if !*force {
		if latest == current {
			fmt.Println("Already on", version.Version)
			return nil
		}
		// The latest stable release can be older than a beta running here: going back is the
		// user's choice, never what a plain update does.
		if *tag == "" && version.Newer(current, latest) {
			fmt.Printf("This hub runs %s, which is newer than the latest stable release %s.\n", version.Version, rel.TagName)
			fmt.Printf("To go back to it: nextboard update --version %s --force\n", rel.TagName)
			return nil
		}
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	if err := client.Install(ctx, rel, self, nil); err != nil {
		return err
	}
	fmt.Printf("Updated %s → %s (the previous binary is kept as %s)\n", version.Version, rel.TagName, update.OldPath(self))
	if err := restartService(); err != nil {
		fmt.Fprintln(os.Stderr, "warning: service restart failed:", err)
	}
	return nil
}
