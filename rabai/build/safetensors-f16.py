"""Converts the F32 tensors of a safetensors file to F16."""
import json, struct, sys
import numpy as np
src, dst = sys.argv[1], sys.argv[2]
b = open(src, "rb").read()
n = struct.unpack("<Q", b[:8])[0]
hdr = json.loads(b[8:8 + n])
data = b[8 + n:]
out_hdr, chunks, off = {}, [], 0
for k, v in hdr.items():
    if k == "__metadata__":
        out_hdr[k] = v
        continue
    s, e = v["data_offsets"]
    raw = data[s:e]
    if v["dtype"] == "F32":
        raw = np.frombuffer(raw, dtype="<f4").astype("<f2").tobytes()
        v = {**v, "dtype": "F16"}
    out_hdr[k] = {**v, "data_offsets": [off, off + len(raw)]}
    chunks.append(raw)
    off += len(raw)
h = json.dumps(out_hdr, separators=(",", ":")).encode()
h += b" " * ((8 - len(h) % 8) % 8)
open(dst, "wb").write(struct.pack("<Q", len(h)) + h + b"".join(chunks))
print(dst, off)
