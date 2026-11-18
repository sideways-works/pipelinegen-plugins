"""The `bazel run` targets of a plugin or template, each the launcher (tools/launch) with its command.

A plugin's :test and :lint are slices of this repository's own CI (pipelinegen.yaml): each takes the
plugin's files as data, so a change to them selects it, and carries the tag of its suite.
"""

load("@rules_go//go:def.bzl", "go_binary")

_TOOLS = [
    "--go=$(rlocationpath @go_sdk//:bin/go)",
    "--lint=$(rlocationpath //tools:golangci-lint)",
    "--node=$(rlocationpath //tools:node)",
    "--gh=$(rlocationpath //tools:gh)",
]

_DATA = [
    "//tools:gh",
    "//tools:golangci-lint",
    "//tools:node",
    "@go_sdk//:bin/go",
    "@go_sdk//:files",
]

def launcher(name, args, data = [], **kwargs):
    """A target that runs the launcher with the pinned tools and args.

    Args:
      name: the target's name.
      args: the launcher's command and its arguments.
      data: files the command reads, besides the tools.
      **kwargs: passed to go_binary.
    """
    go_binary(
        name = name,
        args = _TOOLS + args,
        data = _DATA + data,
        embed = ["//tools/cmd/launch:lib"],
        **kwargs
    )

def _unit(dir, tags):
    launcher(
        name = "test",
        args = ["test", dir],
        data = [":files"],
        tags = tags,
    )
    launcher(
        name = "lint",
        args = ["lint", dir],
        data = [":files", "//:lint-config"],
        tags = ["slice-plugins-lint"],
    )

def plugin(dir, tags):
    """:test, :lint and :release of the plugin in dir (KIND/NAME), its files in the package's :files.

    Args:
      dir: the plugin's directory, this package's.
      tags: the tag of the CI suite of its tests, its kind's: ["slice-plugins-KIND"].
    """
    _unit(dir = dir, tags = tags)
    launcher(name = "release", args = ["release", dir], data = [":files"])

def template(dir):
    """:test and :lint of the template in dir (templates/KIND), its files in the package's :files.

    Args:
      dir: the template's directory, this package's.
    """
    _unit(dir = dir, tags = ["slice-plugins-templates"])
