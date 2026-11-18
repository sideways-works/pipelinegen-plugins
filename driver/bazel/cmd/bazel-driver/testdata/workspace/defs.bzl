"""Shell scripts as Bazel tests and libraries, without rules_shell.

script_test runs a script; script_library holds the scripts tests source,
and is what `bazel coverage` instruments: a test target's own files are
not instrumented, so its coverage keeps only its libraries' records.
"""

def _script_library(ctx):
    return [
        DefaultInfo(files = depset(ctx.files.srcs), runfiles = ctx.runfiles(files = ctx.files.srcs)),
        coverage_common.instrumented_files_info(ctx, source_attributes = ["srcs"]),
    ]

script_library = rule(
    implementation = _script_library,
    attrs = {"srcs": attr.label_list(allow_files = [".sh"])},
)

def _script_test(ctx):
    # a launcher, so the script needs no executable bit; a test runs from
    # its runfiles' root, where the script's short path leads
    exe = ctx.actions.declare_file(ctx.label.name)
    ctx.actions.write(exe, "#!/bin/sh\nexec sh %s \"$@\"\n" % ctx.file.src.short_path, is_executable = True)
    runfiles = ctx.runfiles(files = [ctx.file.src])
    for dep in ctx.attr.deps:
        runfiles = runfiles.merge(dep[DefaultInfo].default_runfiles)
    return [
        DefaultInfo(executable = exe, runfiles = runfiles),
        coverage_common.instrumented_files_info(ctx, dependency_attributes = ["deps"]),
    ]

script_test = rule(
    implementation = _script_test,
    test = True,
    attrs = {
        "src": attr.label(allow_single_file = [".sh"], mandatory = True),
        "deps": attr.label_list(),
        # what joins a test's coverage files into its coverage.dat under
        # `bazel coverage`: a test rule without it leaves the file empty
        "_lcov_merger": attr.label(
            default = configuration_field(fragment = "coverage", name = "output_generator"),
            executable = True,
            cfg = "exec",
        ),
    },
)
