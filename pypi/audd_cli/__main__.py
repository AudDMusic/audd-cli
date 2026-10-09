"""Run the bundled audd binary with this process's arguments."""

import os
import stat
import sys

from audd_cli import binary_path


def main():
    exe = binary_path()
    if not os.path.isfile(exe):
        sys.stderr.write(
            "audd: the binary for this system is missing from the audd-cli package (%s).\n"
            "Reinstall it with: pip install --force-reinstall audd-cli\n" % exe
        )
        return 1
    args = [exe] + sys.argv[1:]
    if os.name == "nt":
        import signal
        import subprocess

        # Ctrl-C reaches audd directly; let it stop cleanly and report its own
        # exit code instead of this wrapper dying first.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        return subprocess.call(args)
    mode = os.stat(exe).st_mode
    if not mode & stat.S_IXUSR:
        try:
            os.chmod(exe, mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
        except OSError:
            pass
    os.execv(exe, args)
    return 1  # not reached


if __name__ == "__main__":
    sys.exit(main())
