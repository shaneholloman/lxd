package main

import (
	"errors"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/canonical/lxd/shared/api"
	cli "github.com/canonical/lxd/shared/cmd"
	"github.com/canonical/lxd/shared/i18n"
)

type cmdImageAlias struct {
	global *cmdGlobal
	image  *cmdImage
}

func (c *cmdImageAlias) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("alias")
	cmd.Short = i18n.G("Manage image aliases")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(
		`Manage image aliases`))

	// Create
	imageAliasCreateCmd := cmdImageAliasCreate{global: c.global, image: c.image, imageAlias: c}
	cmd.AddCommand(imageAliasCreateCmd.command())

	// Delete
	imageAliasDeleteCmd := cmdImageAliasDelete{global: c.global, image: c.image, imageAlias: c}
	cmd.AddCommand(imageAliasDeleteCmd.command())

	// List
	imageAliasListCmd := cmdImageAliasList{global: c.global, image: c.image, imageAlias: c}
	cmd.AddCommand(imageAliasListCmd.command())

	// Rename
	imageAliasRenameCmd := cmdImageAliasRename{global: c.global, image: c.image, imageAlias: c}
	cmd.AddCommand(imageAliasRenameCmd.command())

	// Workaround for subcommand usage errors. See: https://github.com/spf13/cobra/issues/706
	cmd.Args = cobra.NoArgs
	cmd.Run = func(cmd *cobra.Command, args []string) { _ = cmd.Usage() }
	return cmd
}

// Create.
type cmdImageAliasCreate struct {
	global     *cmdGlobal
	image      *cmdImage
	imageAlias *cmdImageAlias
}

func (c *cmdImageAliasCreate) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("create", i18n.G("[<remote>:]<alias> <fingerprint>"))
	cmd.Short = i18n.G("Create aliases for existing images")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(
		`Create aliases for existing images`))

	cmd.RunE = c.run

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 1 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		if len(args) == 0 {
			return c.global.cmpRemotes(toComplete, ":", true, instanceServerRemoteCompletionFilters(*c.global.conf)...)
		}

		remote, _, err := c.global.conf.ParseRemote(args[0])
		if err != nil {
			return handleCompletionError(err)
		}

		return c.global.cmpTopLevelResourceInRemote(remote, "image", toComplete)
	}

	return cmd
}

func (c *cmdImageAliasCreate) run(cmd *cobra.Command, args []string) error {
	// Quick checks.
	exit, err := c.global.CheckArgs(cmd, args, 2, 2)
	if exit {
		return err
	}

	// Parse remote
	resources, err := c.global.ParseServers(args[0])
	if err != nil {
		return err
	}

	resource := resources[0]

	if resource.name == "" {
		return errors.New(i18n.G("Alias name missing"))
	}

	// Create the alias
	alias := api.ImageAliasesPost{}
	alias.Name = resource.name
	alias.Target = args[1]

	return resource.server.CreateImageAlias(alias)
}

// Delete.
type cmdImageAliasDelete struct {
	global     *cmdGlobal
	image      *cmdImage
	imageAlias *cmdImageAlias
}

func (c *cmdImageAliasDelete) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("delete", i18n.G("[<remote>:]<alias>"))
	cmd.Aliases = []string{"rm"}
	cmd.Short = i18n.G("Delete image aliases")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(
		`Delete image aliases`))

	cmd.RunE = c.run

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		return c.global.cmpImages(toComplete, true)
	}

	return cmd
}

func (c *cmdImageAliasDelete) run(cmd *cobra.Command, args []string) error {
	// Quick checks.
	exit, err := c.global.CheckArgs(cmd, args, 1, 1)
	if exit {
		return err
	}

	// Parse remote
	resources, err := c.global.ParseServers(args[0])
	if err != nil {
		return err
	}

	resource := resources[0]

	if resource.name == "" {
		return errors.New(i18n.G("Alias name missing"))
	}

	// Delete the alias
	return resource.server.DeleteImageAlias(resource.name)
}

// List.
type cmdImageAliasList struct {
	global     *cmdGlobal
	image      *cmdImage
	imageAlias *cmdImageAlias

	flagFormat string
}

func (c *cmdImageAliasList) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("list", i18n.G("[<remote>:] [<filters>...]"))
	cmd.Aliases = []string{"ls"}
	cmd.Short = i18n.G("List image aliases")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(
		`List image aliases

Filters may be part of the image hash or part of the image alias name.
`))
	cmd.Flags().StringVarP(&c.flagFormat, "format", "f", "table", i18n.G("Format (csv|json|table|yaml|compact)")+"``")

	cmd.RunE = c.run

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		return c.global.cmpRemotes(toComplete, ":", true, imageServerRemoteCompletionFilters(*c.global.conf)...)
	}

	return cmd
}

func (c *cmdImageAliasList) aliasShouldShow(filters []string, state *api.ImageAliasesEntry) bool {
	if len(filters) == 0 {
		return true
	}

	for _, filter := range filters {
		if strings.Contains(state.Name, filter) || strings.Contains(state.Target, filter) {
			return true
		}
	}

	return false
}

func (c *cmdImageAliasList) run(cmd *cobra.Command, args []string) error {
	// Quick checks.
	exit, err := c.global.CheckArgs(cmd, args, 0, -1)
	if exit {
		return err
	}

	// Parse remote
	remote := ""
	if len(args) > 0 {
		remote = args[0]
	}

	remoteName, name, err := c.global.conf.ParseRemote(remote)
	if err != nil {
		return err
	}

	remoteServer, err := c.global.conf.GetImageServer(remoteName)
	if err != nil {
		return err
	}

	// Process the filters
	filters := []string{}
	if name != "" {
		filters = append(filters, name)
	}

	if len(args) > 1 {
		filters = append(filters, args[1:]...)
	}

	// List the aliases
	aliases, err := remoteServer.GetImageAliases()
	if err != nil {
		return err
	}

	// Render the table
	data := [][]string{}
	for _, alias := range aliases {
		if !c.aliasShouldShow(filters, &alias) {
			continue
		}

		if alias.Type == "" {
			alias.Type = "container"
		}

		data = append(data, []string{alias.Name, alias.Target[0:12], strings.ToUpper(alias.Type), alias.Description})
	}

	sort.Sort(cli.StringList(data))

	header := []string{
		i18n.G("ALIAS"),
		i18n.G("FINGERPRINT"),
		i18n.G("TYPE"),
		i18n.G("DESCRIPTION"),
	}

	return cli.RenderTable(c.flagFormat, header, data, aliases)
}

// Rename.
type cmdImageAliasRename struct {
	global     *cmdGlobal
	image      *cmdImage
	imageAlias *cmdImageAlias
}

func (c *cmdImageAliasRename) command() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Use = usage("rename", i18n.G("[<remote>:]<alias> <new-name>"))
	cmd.Aliases = []string{"mv"}
	cmd.Short = i18n.G("Rename aliases")
	cmd.Long = cli.FormatSection(i18n.G("Description"), i18n.G(
		`Rename aliases`))

	cmd.RunE = c.run

	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		return c.global.cmpImages(toComplete, true)
	}

	return cmd
}

func (c *cmdImageAliasRename) run(cmd *cobra.Command, args []string) error {
	// Quick checks.
	exit, err := c.global.CheckArgs(cmd, args, 2, 2)
	if exit {
		return err
	}

	// Parse remote
	resources, err := c.global.ParseServers(args[0])
	if err != nil {
		return err
	}

	resource := resources[0]

	if resource.name == "" {
		return errors.New(i18n.G("Alias name missing"))
	}

	// Rename the alias
	return resource.server.RenameImageAlias(resource.name, api.ImageAliasesEntryPost{Name: args[1]})
}
