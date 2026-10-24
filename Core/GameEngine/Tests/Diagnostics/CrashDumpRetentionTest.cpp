// GeneralsX @bugfix Codex 24/10/2026 Exercise paired dump retention, standalone reports, and deletion retry on real files.
#include "Platform/CrashDumpRetention.h"
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <cstdio>
#include <string>

namespace
{
int failures = 0;
void check(bool condition, const char* reason)
{
    if (!condition)
    {
        std::fprintf(stderr, "FAIL: %s (Windows error %lu)\n", reason, GetLastError());
        ++failures;
    }
}

bool exists(const std::string& path)
{
    return GetFileAttributesA(path.c_str()) != INVALID_FILE_ATTRIBUTES;
}

void create(const std::string& path, unsigned int order)
{
    HANDLE file = CreateFileA(path.c_str(), GENERIC_WRITE, 0, nullptr, CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
    check(file != INVALID_HANDLE_VALUE, "create retention fixture");
    if (file == INVALID_HANDLE_VALUE)
        return;
    // Whole seconds apart, with a valid FILETIME epoch, so enumeration order cannot affect retention.
    ULARGE_INTEGER value;
    value.QuadPart = 132000000000000000ULL + static_cast<ULONGLONG>(order) * 10000000;
    FILETIME time = { value.LowPart, value.HighPart };
    check(SetFileTime(file, nullptr, nullptr, &time) != FALSE, "timestamp retention fixture");
    CloseHandle(file);
}

void cleanup(const std::string& directory)
{
    WIN32_FIND_DATAA data;
    HANDLE search = FindFirstFileA((directory + "*").c_str(), &data);
    if (search != INVALID_HANDLE_VALUE)
    {
        do
        {
            if (!(data.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY))
                DeleteFileA((directory + data.cFileName).c_str());
        } while (FindNextFileA(search, &data));
        FindClose(search);
    }
}
}

int main()
{
    char temporary[MAX_PATH];
    char folder[MAX_PATH];
    if (!GetTempPathA(MAX_PATH, temporary) || !GetTempFileNameA(temporary, "gxr", 0, folder))
        return 1;
    DeleteFileA(folder);
    if (!CreateDirectoryA(folder, nullptr))
        return 1;
    const std::string directory = std::string(folder) + "\\";

    create(directory + "CrashMZ-new.dmp", 50);
    create(directory + "CrashMZ-new.dmp.txt", 51);
    create(directory + "CrashMZ-second.dmp", 40);
    create(directory + "CrashMZ-old.dmp", 30);
    create(directory + "CrashMZ-old.dmp.txt", 31);
    create(directory + "CrashMZ-orphan.dmp.txt", 20);
    create(directory + "CrashMZ-retry.dmp", 10);
    create(directory + "CrashMZ-retry.dmp.txt", 11);
    create(directory + "CrashFZ-unrelated.dmp", 1);
    create(directory + "CrashMZ-unrelated.dmp.bak", 1);
    create(directory + "notes.txt", 1);

    const std::string retryReport = directory + "CrashMZ-retry.dmp.txt";
    HANDLE locked = CreateFileA(retryReport.c_str(), GENERIC_READ, FILE_SHARE_READ | FILE_SHARE_WRITE,
        nullptr, OPEN_EXISTING, FILE_ATTRIBUTE_NORMAL, nullptr);
    check(locked != INVALID_HANDLE_VALUE, "lock old report against deletion");
    KeepNewestCrashCaptures(directory.c_str(), 'M', 2);
    check(exists(directory + "CrashMZ-new.dmp") && exists(directory + "CrashMZ-new.dmp.txt") &&
        exists(directory + "CrashMZ-second.dmp"), "paired sidecar does not displace a retained dump");
    check(!exists(directory + "CrashMZ-old.dmp") && !exists(directory + "CrashMZ-old.dmp.txt"),
        "remove both files for an expired capture");
    check(!exists(directory + "CrashMZ-orphan.dmp.txt"), "prune standalone CreateFile-failure report");
    check(!exists(directory + "CrashMZ-retry.dmp") && exists(retryReport),
        "failed report deletion leaves an orphan that can be retried");
    if (locked != INVALID_HANDLE_VALUE)
        CloseHandle(locked);
    KeepNewestCrashCaptures(directory.c_str(), 'M', 2);
    check(!exists(retryReport), "next retention pass deletes the previously locked orphan");
    check(exists(directory + "CrashFZ-unrelated.dmp") && exists(directory + "CrashMZ-unrelated.dmp.bak") &&
        exists(directory + "notes.txt"), "leave other dump types and unrelated files untouched");

    cleanup(directory);
    create(directory + "CrashMZ-orphan1.dmp.txt", 1);
    create(directory + "CrashMZ-orphan2.dmp.txt", 2);
    create(directory + "CrashMZ-orphan3.dmp.txt", 3);
    create(directory + "CrashMZ-orphan4.dmp.txt", 4);
    KeepNewestCrashCaptures(directory.c_str(), 'M', 2);
    check(!exists(directory + "CrashMZ-orphan1.dmp.txt") && !exists(directory + "CrashMZ-orphan2.dmp.txt") &&
        exists(directory + "CrashMZ-orphan3.dmp.txt") && exists(directory + "CrashMZ-orphan4.dmp.txt"),
        "bound an orphan-only directory and preserve the newest reports");
    cleanup(directory);
    check(RemoveDirectoryA(folder) != FALSE, "remove temporary test directory");
    return failures == 0 ? 0 : 1;
}
