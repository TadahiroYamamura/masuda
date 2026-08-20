package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	masuda "github.com/TadahiroYamamura/masuda"
	"github.com/TadahiroYamamura/masuda/internal/config"
	"github.com/TadahiroYamamura/masuda/internal/selfupdate"
)

// newImageCommand groups commands for this repository's VM image entries
// (.masuda/images/<entry>/, ADR-0054). `masuda init` materializes the one
// entry every project needs -- the main sandbox VM's -- and this is how any
// further entry gets created, notably the single-purpose image a privileged
// command runs in (ADR-0053).
//
// Deliberately an explicit, opt-in step rather than something `masuda init`
// does for everyone: every declared entry is an image `masuda update` then
// rebuilds, and a project that never runs a privileged command should not
// pay for one. The declaration side stays honest either way -- a privileged
// command's declaration names an entry, and that entry exists because
// someone ran this command.
func newImageCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage this repository's VM image entries (.masuda/images/)",
	}
	cmd.AddCommand(newImageAddCommand())
	cmd.AddCommand(newImageListCommand())
	return cmd
}

// imageTemplates maps --template values to the starting Dockerfile content.
// Both live in this CLI binary rather than being fetched: unlike the review
// perspectives (ADR-0033), which are synced during `masuda update` and so
// would lag a release behind if they were embedded, a template is only ever
// read by a command the user runs on its own -- by which point the binary
// holding it is already the updated one.
const (
	templateDefault = "default"
	templateDocker  = "docker"
)

func newImageAddCommand() *cobra.Command {
	var template string
	cmd := &cobra.Command{
		Use:   "add <entry-name>",
		Short: "Create a new .masuda/images/ entry from a starting template",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			entry := args[0]
			if err := config.ValidateImageEntry(entry); err != nil {
				return err
			}
			root, err := repoRoot()
			if err != nil {
				return err
			}
			// Never overwrite: the Dockerfile is the user's file the moment
			// it lands (ADR-0024's materialize-once pattern), and this
			// command has no way to tell a customized one from an untouched
			// one.
			if _, err := os.Stat(config.ImageDir(root, entry)); err == nil {
				return fmt.Errorf("%s already exists", config.ImageDir(root, entry))
			}

			dockerfile, err := imageTemplate(template)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(config.ImageDir(root, entry), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(config.ImageDockerfilePath(root, entry), dockerfile, 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(config.ImageSettingsPath(root, entry), []byte("{}\n"), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s\nedit its Dockerfile, then run `masuda sandbox build` to build it\n",
				config.ImageDir(root, entry))
			return nil
		},
	}
	cmd.Flags().StringVar(&template, "template", templateDefault, "starting content: \"default\" (a sandbox VM image) or \"docker\" (a VM that runs a Docker daemon, for privileged commands)")
	return cmd
}

// imageTemplate returns the starting Dockerfile for a template name. The
// default template pins its FROM to a published masuda release, so it needs
// the network to find the current tag; the docker one derives from ubuntu
// directly (ADR-0054) and needs nothing.
func imageTemplate(template string) ([]byte, error) {
	switch template {
	case templateDocker:
		return masuda.DockerTemplate, nil
	case templateDefault:
		release, err := selfupdate.FetchLatestRelease(selfupdate.DefaultAPIBase, selfupdate.DefaultRepo)
		if err != nil {
			return nil, err
		}
		return []byte(dockerfileTemplate(release.TagName)), nil
	default:
		return nil, fmt.Errorf("unknown --template %q (want %q or %q)", template, templateDefault, templateDocker)
	}
}

func newImageListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List this repository's image entries and the local Docker tag each builds as",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := repoRoot()
			if err != nil {
				return err
			}
			entries, err := config.ListImageEntries(root)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "no image entries under %s\n", config.ImagesDir(root))
				return nil
			}
			for _, entry := range entries {
				tag, err := config.ImageTag(root, entry)
				if err != nil {
					return err
				}
				imageCfg, err := config.LoadImage(root, entry)
				if err != nil {
					return err
				}
				size := "auto"
				if imageCfg.RootfsSizeMiB > 0 {
					size = fmt.Sprintf("%d MiB", imageCfg.RootfsSizeMiB)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\trootfs=%s\n", entry, tag, size)
			}
			return nil
		},
	}
}
