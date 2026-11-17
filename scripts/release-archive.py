#!/usr/bin/env python3
"""Write one release archive the same way on every machine: scripts/release-dist.sh calls this.

  release-archive.py OUT TOPDIR MTIME FILE...

OUT ends in .tar.gz or .zip. The files go under TOPDIR/ in the order given. Every entry gets the same
modification time (MTIME, seconds since the epoch), owner 0:0 and a fixed mode: 0755 for a file whose name
starts with `scriobh` (the programs), 0644 for the rest. The gzip header carries no time and no file name.
So the bytes depend only on the files' contents, the order and the compressor (zlib), not on the machine,
the user, the umask or the clock.

Python's tarfile and zipfile, not tar and zip: BSD and GNU tar differ in what they record, and zip is not
on every runner image.
"""
import gzip
import os
import stat
import sys
import tarfile
import zipfile


def mode_of(path):
    return 0o755 if os.path.basename(path).startswith("scriobh") else 0o644


def write_tar(out, top, mtime, files):
    with open(out, "wb") as raw:
        with gzip.GzipFile(filename="", mode="wb", fileobj=raw, compresslevel=9, mtime=0) as gz:
            with tarfile.open(fileobj=gz, mode="w", format=tarfile.USTAR_FORMAT) as tar:
                d = tarfile.TarInfo(top + "/")
                d.type, d.mode, d.mtime = tarfile.DIRTYPE, 0o755, mtime
                tar.addfile(d)
                for path in files:
                    ti = tarfile.TarInfo(top + "/" + os.path.basename(path))
                    ti.size = os.path.getsize(path)
                    ti.mode, ti.mtime = mode_of(path), mtime
                    with open(path, "rb") as fh:
                        tar.addfile(ti, fh)


def write_zip(out, top, mtime, files):
    import time
    t = time.gmtime(max(mtime, 315532800))  # zip cannot hold a date before 1980
    stamp = (t.tm_year, t.tm_mon, t.tm_mday, t.tm_hour, t.tm_min, t.tm_sec - t.tm_sec % 2)
    with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for path in files:
            zi = zipfile.ZipInfo(top + "/" + os.path.basename(path), stamp)
            zi.compress_type = zipfile.ZIP_DEFLATED
            zi.create_system = 3  # Unix, whatever machine this runs on
            zi.external_attr = (stat.S_IFREG | mode_of(path)) << 16
            with open(path, "rb") as fh:
                z.writestr(zi, fh.read(), compresslevel=9)


def main(argv):
    if len(argv) < 5:
        sys.exit(__doc__)
    out, top, mtime, files = argv[1], argv[2], int(argv[3]), argv[4:]
    for f in files:
        if not os.path.isfile(f):
            sys.exit("release-archive: no such file: " + f)
    if out.endswith(".tar.gz"):
        write_tar(out, top, mtime, files)
    elif out.endswith(".zip"):
        write_zip(out, top, mtime, files)
    else:
        sys.exit("release-archive: OUT must end in .tar.gz or .zip")


main(sys.argv)
