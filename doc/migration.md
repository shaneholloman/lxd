(migration)=
# Migration

```{youtube} https://www.youtube.com/watch?v=F9GALjHtnUU
```

LXD provides tools and functionality to migrate instances in different contexts.

Migrate physical or virtual machines to LXD instances
: If you have an existing machine, either physical or virtual (VM or container), you can use the `lxd-migrate` tool to create a LXD instance based on your existing machine.
  The tool copies the provided partition, disk or image to the LXD storage pool of the provided LXD server, sets up an instance using that storage and allows you to configure additional settings for the new instance.

  See {ref}`import-machines-to-instances` for more information.

Migrate existing LXD instances between servers
: The most basic kind of migration is if you have a LXD instance on one server and want to move it to a different LXD server.
  For virtual machines, you can do that as a live migration, which means that you can migrate your VM while it is running and there will be no downtime.

  See {ref}`move-instances` for more information.

````{only} diataxis
The following how-to guides cover common operations related to migration:
````

```{filtered-toctree}
:titlesonly:

:diataxis:Import existing machines </howto/import_machines_to_instances>
:diataxis:Move instances </howto/move_instances>
```

```{filtered-toctree}
:maxdepth: 1
:hidden:

:topical:Move instances </howto/move_instances>
:topical:Import existing machines </howto/import_machines_to_instances>
```
