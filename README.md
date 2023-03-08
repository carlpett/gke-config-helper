# gke-config-helper

A small utility to generate a `kubectl` configuration file for all clusters you have access to in GKE.

# Installation

1. Download latest [release](github.com/carlpett/gke-config-helper)
2. Rename to file to `gke-config-helper`
3. Convert into binary `chmod +x gke-config-helper`

Optionally you could move it into a `/bin/` or add it to the environment `$PATH`


### M1 chip

# Usage

```shell
$ gke-config-helper
``` 

The basic invocation will check each project you have access to, list clusters and output a kubeconfig with a `cluster` and a `context` entry for each of them to stdout.

## Multiple config files

Since you might call this utility on a semi-regular basis, when there are new clusters to interact with, it is suggested to store the output in a separate configuration file if you also connect to non-GKE clusters (eg minikube). `kubectl` supports merging multiple configuration files by using the `KUBECONFIG` environment variable:

```shell
$ gke-config-helper > ~/.kube/gke-config
$ export KUBECONFIG=~/.kube/config:~/.kube/gke-config
```

In this way, other cluster, context and user configurations are not affected by updating the GKE configuration. `kubectl` also stores written values in the first of the files, meaning that `current-context` will remain in the default file and be preserved when regenerating the `gke-config` file.

## Restricting projects

If you have access to a lot of projects, iterating over all of them might be slow. The flag `--search-root` allows you to restrict the search to projects that are children of a folder.

```shell
$ gke-config-helper --search-root my-folder
```

This works to any depth, not only direct decendants of `my-folder`.

## Controlling the context name

By default, the name of each context will be `<projectId>-<clusterName>`. You can control this with the `--context-name-template` flag, which takes a Go template as an argument. Both the [built-in Go functions](https://pkg.go.dev/text/template#hdr-Functions) as well as the [sprig library functions](https://masterminds.github.io/sprig/) are available.

The properties available for templating are:

- `ProjectId`: The project id
- `Name`: Name of the cluster
- `Location`: Region name

For example:
```shell
$ gke-config-helper --context-name-template '{{ slice (.Name | splitList "-") 0 2 | join "-" }}_{{ .Location | substr 0 2 }}'
```

With a cluster `foo-bar-baz` in `europe-west3`, this would generate a context name of `foo-bar_eu`.
