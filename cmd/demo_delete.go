package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/suxess-it/kubrix-cli/internal/install"
	"github.com/suxess-it/kubrix-cli/internal/ui"
)

func newDemoDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete",
		Short: "Delete a demo's kind cluster, or forget an installation; optionally delete its GitHub repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return withUI(cmd, "delete", func(ctx context.Context, u ui.UI) error { return runDemoDelete(ctx, u, productionServices()) })
		},
	}
}

func runDemoDelete(ctx context.Context, u ui.UI, svc services) error {
	catalog := openCatalog(u)
	if err := catalog.Problem(); err != nil {
		return fmt.Errorf("cannot delete from the saved installations: %w", err)
	}
	names := catalog.Keys()
	if len(names) == 0 {
		return errors.New("no saved installation found; nothing to delete")
	}
	name := names[0]
	if len(names) > 1 {
		var options []ui.Option
		for _, n := range names {
			d, _ := catalog.Find(n)
			label := fmt.Sprintf("%s  (%s/%s, kind demo)", n, d.Org, d.Repo)
			if d.IsCluster() {
				label = fmt.Sprintf("%s  (%s/%s, existing cluster)", n, d.Org, d.Repo)
			}
			options = append(options, ui.Option{Label: label, Value: n})
		}
		if err := ui.Select(u, "Which installation?", "", options, &name); err != nil {
			return abortErr(err)
		}
	}
	d, _ := catalog.Find(name)

	var forget bool
	if d.IsCluster() {
		ok, err := ui.Confirm(u, fmt.Sprintf("Forget the installation on context %q?", name),
			"kubriX keeps running in the cluster; only the saved selections are removed. Uninstalling is not supported.", true)
		if err != nil {
			return abortErr(err)
		}
		forget = ok
	} else {
		var err error
		if forget, err = deleteKindCluster(ctx, u, svc.kind, name); err != nil {
			return err
		}
	}

	if d.Org != "" && d.Repo != "" {
		if err := deleteRepo(ctx, u, svc, d.Org, d.Repo); err != nil {
			return err
		}
	}
	if forget {
		return catalog.Forget(name)
	}
	return nil
}

// deleteKindCluster returns true when the cluster no longer exists.
func deleteKindCluster(ctx context.Context, u ui.UI, clusters install.KindClusters, name string) (bool, error) {
	exists, err := clusters.Exists(name)
	if err != nil {
		return false, err
	}
	if !exists {
		u.Info(fmt.Sprintf("kind cluster %q does not exist", name))
		return true, nil
	}
	ok, err := ui.Confirm(u, fmt.Sprintf("Delete kind cluster %q?", name), "", true)
	if err != nil {
		return false, abortErr(err)
	}
	if !ok {
		return false, nil
	}
	if err := u.Step(ctx, "Deleting kind cluster "+name, func(context.Context) error { return clusters.Delete(name) }); err != nil {
		return false, err
	}
	u.OK("deleted kind cluster " + name)
	return true, nil
}

func deleteRepo(ctx context.Context, u ui.UI, svc services, org, repo string) error {
	slug := org + "/" + repo
	ok, err := ui.Confirm(u, "Also delete GitHub repository "+slug+"?", "This cannot be undone. It needs the delete_repo scope (gh: 'gh auth refresh -h github.com -s delete_repo').", false)
	if err != nil {
		return abortErr(err)
	}
	if !ok {
		return nil
	}
	gh, _, err := svc.github(ctx, u)
	if err != nil {
		return err
	}
	if err := u.Step(ctx, "Deleting "+slug, func(ctx context.Context) error { return gh.DeleteRepo(ctx, org, repo) }); err != nil {
		return err
	}
	u.OK("deleted " + slug)
	return nil
}
