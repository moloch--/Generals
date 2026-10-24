// GeneralsX @bugfix Codex 24/10/2026 Verify crash memory telemetry detects and releases real virtual-address pressure.
#include "Platform/CrashMemoryStatus.h"
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <cstring>

namespace
{
bool check(bool condition, const char* reason)
{
    if (!condition)
        std::fprintf(stderr, "FAIL: %s\n", reason);
    return condition;
}
}

int main()
{
    CrashMemoryStatus before;
    CaptureCrashMemoryStatus(before);
    bool valid = check(before.processValid && before.systemValid && before.addressSpaceComplete,
        "capture process, system, and complete virtual address map");
    valid = check(before.privateBytes > 0 && before.physicalTotalBytes > 0 &&
        before.virtualTotalBytes > 0 && before.largestFreeRegionBytes <= before.freeAddressBytes,
        "memory counters have consistent units and bounds") && valid;

    const SIZE_T reserveBytes = 32 * 1024 * 1024;
    void* reservation = VirtualAlloc(nullptr, reserveBytes, MEM_RESERVE, PAGE_NOACCESS);
    if (!check(reservation != nullptr, "reserve test virtual address space"))
        return 1;
    CrashMemoryStatus reserved;
    CaptureCrashMemoryStatus(reserved);
    valid = check(reserved.addressSpaceComplete && before.freeAddressBytes >= reserved.freeAddressBytes + reserveBytes,
        "reservation reduces free virtual addresses without requiring physical memory") && valid;
    valid = check(VirtualFree(reservation, 0, MEM_RELEASE) != FALSE, "release test reservation") && valid;
    CrashMemoryStatus after;
    CaptureCrashMemoryStatus(after);
    valid = check(after.addressSpaceComplete && after.freeAddressBytes >= reserved.freeAddressBytes + reserveBytes,
        "free address count recovers after release") && valid;

    FILE* report = std::tmpfile();
    if (!check(report != nullptr, "open temporary report"))
        return 1;
    WriteCrashMemoryStatus(report, reserved);
    std::rewind(report);
    char text[2048] = {};
    std::fread(text, 1, sizeof(text) - 1, report);
    std::fclose(report);
    valid = check(std::strstr(text, "Process private=") && std::strstr(text, "largest_free_region=") &&
        std::strstr(text, "scan_complete=yes"), "report contains process and fragmentation evidence") && valid;
    return valid ? 0 : 1;
}
