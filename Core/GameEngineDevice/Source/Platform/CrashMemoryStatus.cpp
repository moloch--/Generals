// GeneralsX @bugfix Codex 24/10/2026 Record process commit and address-space fragmentation on Windows crashes.
#include "Platform/CrashMemoryStatus.h"

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <psapi.h>

void CaptureCrashMemoryStatus(CrashMemoryStatus& status)
{
    status = {};
    PROCESS_MEMORY_COUNTERS_EX process = {};
    process.cb = sizeof(process);
    status.processValid = GetProcessMemoryInfo(GetCurrentProcess(),
        reinterpret_cast<PROCESS_MEMORY_COUNTERS*>(&process), sizeof(process)) != FALSE;
    if (status.processValid)
    {
        status.privateBytes = process.PrivateUsage;
        status.workingSetBytes = process.WorkingSetSize;
        status.peakWorkingSetBytes = process.PeakWorkingSetSize;
    }
    else
        status.processError = GetLastError();

    MEMORYSTATUSEX system = {};
    system.dwLength = sizeof(system);
    status.systemValid = GlobalMemoryStatusEx(&system) != FALSE;
    if (status.systemValid)
    {
        status.physicalTotalBytes = system.ullTotalPhys;
        status.physicalAvailableBytes = system.ullAvailPhys;
        status.commitTotalBytes = system.ullTotalPageFile;
        status.commitAvailableBytes = system.ullAvailPageFile;
        status.virtualTotalBytes = system.ullTotalVirtual;
        status.virtualAvailableBytes = system.ullAvailVirtual;
    }
    else
        status.systemError = GetLastError();

    SYSTEM_INFO systemInfo = {};
    GetSystemInfo(&systemInfo);
    std::uintptr_t cursor = reinterpret_cast<std::uintptr_t>(systemInfo.lpMinimumApplicationAddress);
    const std::uintptr_t maximum = reinterpret_cast<std::uintptr_t>(systemInfo.lpMaximumApplicationAddress);
    while (cursor <= maximum)
    {
        MEMORY_BASIC_INFORMATION region = {};
        if (VirtualQuery(reinterpret_cast<const void*>(cursor), &region, sizeof(region)) == 0)
        {
            status.addressSpaceError = GetLastError();
            return;
        }
        // Widen before addition so a region ending at 4 GiB cannot wrap an x86 pointer.
        const std::uint64_t next = static_cast<std::uint64_t>(reinterpret_cast<std::uintptr_t>(region.BaseAddress)) + region.RegionSize;
        if (next <= cursor)
        {
            status.addressSpaceError = ERROR_INVALID_DATA;
            return;
        }
        if (region.State == MEM_FREE)
        {
            status.freeAddressBytes += region.RegionSize;
            if (region.RegionSize > status.largestFreeRegionBytes)
                status.largestFreeRegionBytes = region.RegionSize;
        }
        if (next > maximum)
        {
            status.addressSpaceComplete = true;
            return;
        }
        cursor = static_cast<std::uintptr_t>(next);
    }
    status.addressSpaceComplete = true;
}

void WriteCrashMemoryStatus(FILE* output, const CrashMemoryStatus& status)
{
    if (!output)
        return;
    std::fprintf(output, "\nMemory status before crash reporting (bytes; snapshot is not a diagnosis):\n");
    if (status.processValid)
        std::fprintf(output, "Process private=%llu working_set=%llu peak_working_set=%llu\n",
            static_cast<unsigned long long>(status.privateBytes),
            static_cast<unsigned long long>(status.workingSetBytes),
            static_cast<unsigned long long>(status.peakWorkingSetBytes));
    else
        std::fprintf(output, "Process memory query failed: Windows error %lu\n", status.processError);
    if (status.systemValid)
        std::fprintf(output, "Physical total=%llu available=%llu; commit limit=%llu available=%llu; process virtual total=%llu available=%llu\n",
            static_cast<unsigned long long>(status.physicalTotalBytes),
            static_cast<unsigned long long>(status.physicalAvailableBytes),
            static_cast<unsigned long long>(status.commitTotalBytes),
            static_cast<unsigned long long>(status.commitAvailableBytes),
            static_cast<unsigned long long>(status.virtualTotalBytes),
            static_cast<unsigned long long>(status.virtualAvailableBytes));
    else
        std::fprintf(output, "System memory query failed: Windows error %lu\n", status.systemError);
    std::fprintf(output, "Address space free=%llu largest_free_region=%llu scan_complete=%s Windows_error=%lu\n",
        static_cast<unsigned long long>(status.freeAddressBytes),
        static_cast<unsigned long long>(status.largestFreeRegionBytes),
        status.addressSpaceComplete ? "yes" : "no", status.addressSpaceError);
}
