#!/usr/bin/env python3
"""Copy the helper into a disposable checkout of the pinned upstream source."""
import pathlib, shutil, subprocess, sys
root=pathlib.Path(sys.argv[1]).resolve()
commit='89df141912e96d127421de6e4d36e6e2190d9949'
if subprocess.check_output(['git','-C',str(root),'rev-parse','HEAD'],text=True).strip()!=commit:
    raise SystemExit('source commit mismatch')
manifest=root/'Cargo.toml'
text=manifest.read_text()
name='operator-cache-helper'
if name not in text:
    text=text.replace('members = [','members = [\n    "operator-cache-helper",',1)
manifest.write_text(text)
shutil.copytree(pathlib.Path(__file__).resolve().parents[1]/'helpers/cache-mount',root/name,dirs_exist_ok=True)

lock=root/'Cargo.lock'
text=lock.read_text()
if 'name = "curvine-cache-mount"' not in text:
    block='[[package]]\nname = "curvine-cache-mount"\nversion = "0.1.0"\ndependencies = [\n "curvine-config",\n "curvine-fs-api",\n "curvine-model",\n "curvine-runtime",\n "curvine-unified-fs",\n "serde_json",\n "sha2 0.10.9",\n "tokio",\n]\n\n'
    marker='[[package]]\nname = "curvine-cli"'
    if marker not in text: raise SystemExit('unexpected upstream lockfile')
    lock.write_text(text.replace(marker,block+marker,1))
