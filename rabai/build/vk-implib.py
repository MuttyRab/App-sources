"""Writes vulkan-1.def listing the functions the Vulkan loader exports, taken
from the Vulkan registry (core commands plus Win32/WSI extension commands the
Windows loader exports)."""
import sys
import xml.etree.ElementTree as ET

reg = ET.parse(sys.argv[1]).getroot()
out = sys.argv[2]

# Commands that belong to core versions.
core = set()
for feat in reg.findall("feature"):
    apis = (feat.get("api") or "vulkan").split(",")
    if "vulkan" not in apis:
        continue
    for req in feat.findall("require"):
        for c in req.findall("command"):
            core.add(c.get("name"))

# The Windows loader also exports these WSI extensions directly.
wsi_exts = {"VK_KHR_surface", "VK_KHR_swapchain", "VK_KHR_display", "VK_KHR_display_swapchain",
            "VK_KHR_win32_surface", "VK_KHR_get_physical_device_properties2"}
for ext in reg.find("extensions").findall("extension"):
    if ext.get("name") in {"VK_KHR_surface", "VK_KHR_swapchain", "VK_KHR_display", "VK_KHR_display_swapchain", "VK_KHR_win32_surface"}:
        for req in ext.findall("require"):
            for c in req.findall("command"):
                core.add(c.get("name"))

# Drop aliases that the loader exports under the core name only (aliases are
# still exported by modern loaders, but keep the list to real names).
with open(out, "w") as f:
    f.write("LIBRARY vulkan-1.dll\nEXPORTS\n")
    for name in sorted(core):
        f.write(f"  {name}\n")
print(len(core), "exports")
