"""C/C++ configurations for the pinned Zig native and cross compilers."""

load("@rules_cc//cc:cc_toolchain_config_lib.bzl", "tool_path")
load("@rules_cc//cc/common:cc_common.bzl", "cc_common")
load("@rules_cc//cc/toolchains:cc_toolchain_config_info.bzl", "CcToolchainConfigInfo")


def _impl(ctx):
    """Describe the pinned CGO compiler and archiver, rejecting unsupported standalone tools."""
    archiver = ctx.executable.archiver.basename
    compiler = ctx.executable.compiler_file.basename
    unsupported_tool = ctx.executable.unsupported_tool.basename
    return cc_common.create_cc_toolchain_config_info(
        ctx = ctx,
        abi_libc_version = "glibc",
        abi_version = ctx.attr.target_cpu,
        compiler = "clang",
        cxx_builtin_include_directories = [],
        host_system_name = "local",
        target_cpu = ctx.attr.target_cpu,
        target_libc = "glibc",
        target_system_name = ctx.attr.target_system_name,
        tool_paths = [
            tool_path(name = "ar", path = archiver),
            tool_path(name = "cpp", path = compiler),
            tool_path(name = "gcc", path = compiler),
            # The current CGO graph only invokes the compiler and archiver.
            # Fail explicitly if a future target needs another standalone tool.
            tool_path(name = "gcov", path = unsupported_tool),
            tool_path(name = "ld", path = unsupported_tool),
            tool_path(name = "nm", path = unsupported_tool),
            tool_path(name = "objcopy", path = unsupported_tool),
            tool_path(name = "objdump", path = unsupported_tool),
            tool_path(name = "strip", path = unsupported_tool),
        ],
        toolchain_identifier = "dcgm-exporter-{}-zig".format(ctx.attr.target_system_name),
    )


cc_toolchain_config = rule(
    attrs = {
        "archiver": attr.label(
            allow_files = True,
            cfg = "target",
            executable = True,
            mandatory = True,
        ),
        "compiler_file": attr.label(
            allow_files = True,
            cfg = "target",
            executable = True,
            mandatory = True,
        ),
        "target_cpu": attr.string(mandatory = True),
        "target_system_name": attr.string(mandatory = True),
        "unsupported_tool": attr.label(
            allow_files = True,
            cfg = "target",
            executable = True,
            mandatory = True,
        ),
    },
    implementation = _impl,
    provides = [CcToolchainConfigInfo],
)
