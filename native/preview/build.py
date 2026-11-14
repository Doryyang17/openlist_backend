#!/usr/bin/env python3
"""Build the optional preview helper against system FFmpeg development libraries."""
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[2]
output = Path(sys.argv[1]) if len(sys.argv) > 1 else root / "bin/openlist-preview"
output.parent.mkdir(parents=True, exist_ok=True)
libraries = ["libavformat", "libavcodec", "libavutil", "libswscale"]
if shutil.which("pkg-config"):
    flags = shlex.split(subprocess.check_output(
        ["pkg-config", "--cflags", "--libs", *libraries], text=True))
else:
    prefix = Path(os.environ.get("FFMPEG_PREFIX", "/opt/homebrew/opt/ffmpeg"))
    if not (prefix / "include/libavformat/avformat.h").is_file():
        raise SystemExit("Install FFmpeg development libraries and pkg-config, or set FFMPEG_PREFIX")
    flags = ["-I" + str(prefix / "include"), "-L" + str(prefix / "lib"),
             "-Wl,-rpath," + str(prefix / "lib"), *["-l" + lib[3:] for lib in libraries]]
fd, temporary = tempfile.mkstemp(prefix=".preview-build-", dir=output.parent)
os.close(fd)
try:
    subprocess.run([os.environ.get("CC", "cc"), "-std=c11", "-O2", "-Wall", "-Wextra",
                    "-Werror", str(root / "native/preview/main.c"), *flags, "-lm", "-o", temporary], check=True)
    os.chmod(temporary, 0o755)
    os.replace(temporary, output)
finally:
    if os.path.exists(temporary):
        os.unlink(temporary)
print(output)
