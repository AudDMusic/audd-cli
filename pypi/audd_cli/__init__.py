"""The AudD CLI (audd), packaged for pip, pipx, uv, and uvx.

The wheel carries the prebuilt audd binary for one platform; the `audd`
command runs it.
"""

import os
import sys

__version__ = "0.0.0"

__all__ = ["binary_path", "__version__"]


def binary_path():
    """Return the path of the bundled audd binary."""
    name = "audd.exe" if sys.platform == "win32" else "audd"
    return os.path.join(os.path.dirname(os.path.abspath(__file__)), "bin", name)
