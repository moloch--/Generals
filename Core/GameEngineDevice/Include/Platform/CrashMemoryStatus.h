// GeneralsX @bugfix Codex 24/10/2026 Preserve memory pressure evidence before crash reporting allocates memory.
#pragma once

#include <cstdio>
#include <cstdint>

struct CrashMemoryStatus
{
    bool processValid;
    bool systemValid;
    bool addressSpaceComplete;
    unsigned long processError;
    unsigned long systemError;
    unsigned long addressSpaceError;
    std::uint64_t privateBytes;
    std::uint64_t workingSetBytes;
    std::uint64_t peakWorkingSetBytes;
    std::uint64_t physicalTotalBytes;
    std::uint64_t physicalAvailableBytes;
    std::uint64_t commitTotalBytes;
    std::uint64_t commitAvailableBytes;
    std::uint64_t virtualTotalBytes;
    std::uint64_t virtualAvailableBytes;
    std::uint64_t freeAddressBytes;
    std::uint64_t largestFreeRegionBytes;
};

// Uses fixed storage and OS queries; safe to call without the game's allocator.
void CaptureCrashMemoryStatus(CrashMemoryStatus& status);
void WriteCrashMemoryStatus(FILE* output, const CrashMemoryStatus& status);
