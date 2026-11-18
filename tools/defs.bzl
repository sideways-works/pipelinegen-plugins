"""The `bazel run` targets of a plugin or template, each the launcher (tools/launch) with its command."""

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

def launcher(name, args, **kwargs):
    """A target that runs the launcher with the pinned tools and args.

    Args:
      name: the target's name.
      args: the launcher's command and its arguments.
      **kwargs: passed to go_binary.
    """
    go_binary(
        name = name,
        args = _TOOLS + args,
        data = _DATA,
        embed = ["//tools/cmd/launch:lib"],
        **kwargs
    )

def plugin():
    """:test, :lint and :release of the plugin in this package's directory (KIND/NAME)."""
    launcher(name = "test", args = ["test", native.package_name()])
    launcher(name = "lint", args = ["lint", native.package_name()])
    launcher(name = "release", args = ["release", native.package_name()])

def template():
    """:test and :lint of the template in this package's directory (templates/KIND)."""
    launcher(name = "test", args = ["test", native.package_name()])
    launcher(name = "lint", args = ["lint", native.package_name()])
