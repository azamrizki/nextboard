package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/azamrizki/nextboard/internal/instance"
	"github.com/azamrizki/nextboard/internal/update"
	"github.com/azamrizki/nextboard/internal/version"
)

// cmdUpdate replaces this binary with a release, then restarts every instance whose service runs
// it (listed before anything changes), since they all run the one binary. With --instance only
// that instance's service restarts, and the others are named as still on the old build.
func cmdUpdate(args []string) error {
	fs, dir := newFlags("update")
	repo := fs.String("repo", version.Repo, "GitHub repo owner/name")
	force := fs.Bool("force", false, "reinstall even if the version is the same, or older")
	tag := fs.String("version", "", "install this release, such as a beta (v0.4.0-beta.1), instead of the latest stable one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	only := ""
	if flagGiven(fs, "instance") || flagGiven(fs, "data-dir") {
		inst, err := resolveFlags(fs, *dir)
		if err != nil {
			return err
		}
		only = inst.Name
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
	self, err := selfBin()
	if err != nil {
		return err
	}
	services, err := instance.ServicesOn(self)
	if err != nil {
		return err
	}
	var restart, stay []instance.Instance
	for _, in := range services {
		if only == "" || in.Name == only {
			restart = append(restart, in)
		} else {
			stay = append(stay, in)
		}
	}
	if len(restart) > 0 {
		fmt.Printf("After the update these instances restart (local agents relaunch with their resume arguments): %s\n", describeInstances(restart))
	}
	if err := client.Install(ctx, rel, self, nil); err != nil {
		return err
	}
	fmt.Printf("Updated %s → %s (the previous binary is kept as %s)\n", version.Version, rel.TagName, update.OldPath(self))
	for _, in := range restart {
		if err := update.RestartService(in.Name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restarting %s (%s) failed: %v\n", in.Name, in.Service.Name, err)
		}
	}
	switch {
	case len(stay) > 0:
		fmt.Printf("These still run %s until they restart: %s\n", version.Version, describeInstances(stay))
	case len(restart) == 0 && only != "":
		fmt.Printf("No service of instance %s runs %s: restart its hub yourself.\n", only, self)
	case len(restart) == 0:
		fmt.Printf("No service runs %s: restart the hubs that run it yourself.\n", self)
	}
	return nil
}

// describeInstances is "default (127.0.0.1:12312), work (127.0.0.1:12412)".
func describeInstances(ins []instance.Instance) string {
	parts := make([]string, len(ins))
	for i, in := range ins {
		parts[i] = in.Name + " (" + in.Listen + ")"
	}
	return strings.Join(parts, ", ")
}
