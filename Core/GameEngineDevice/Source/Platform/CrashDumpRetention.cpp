// GeneralsX @bugfix Codex 24/10/2026 Include orphaned failure reports in crash-dump retention and retry failed deletion.
#include "Platform/CrashDumpRetention.h"

#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <algorithm>
#include <map>
#include <string>
#include <vector>

namespace
{
struct Capture
{
    std::string dumpPath;
    FILETIME lastWriteTime;
    bool hasDump;
    bool hasReport;

    Capture() : hasDump(false), hasReport(false)
    {
        lastWriteTime.dwLowDateTime = 0;
        lastWriteTime.dwHighDateTime = 0;
    }
};

bool newerCapture(const Capture& left, const Capture& right)
{
    const LONG order = CompareFileTime(&left.lastWriteTime, &right.lastWriteTime);
    return order != 0 ? order > 0 : left.dumpPath > right.dumpPath;
}
}

void KeepNewestCrashCaptures(const char* directory, char dumpType, std::size_t keepCount)
{
    const std::string searchPath = std::string(directory) + "Crash" + dumpType + "*.dmp*";
    WIN32_FIND_DATAA findData;
    HANDLE search = FindFirstFileA(searchPath.c_str(), &findData);
    if (search == INVALID_HANDLE_VALUE)
        return;

    std::map<std::string, Capture> capturesByPath;
    do
    {
        if (findData.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY)
            continue;
        std::string name = findData.cFileName;
        const bool report = name.size() >= 8 && name.compare(name.size() - 8, 8, ".dmp.txt") == 0;
        if (report)
            name.resize(name.size() - 4);
        else if (name.size() < 4 || name.compare(name.size() - 4, 4, ".dmp") != 0)
            continue;

        const std::string dumpPath = std::string(directory) + name;
        Capture& capture = capturesByPath[dumpPath];
        capture.dumpPath = dumpPath;
        // Prefer the dump timestamp for a pair; a standalone report uses its own timestamp.
        if (!report || !capture.hasDump)
            capture.lastWriteTime = findData.ftLastWriteTime;
        if (report)
            capture.hasReport = true;
        else
            capture.hasDump = true;
    } while (FindNextFileA(search, &findData));
    FindClose(search);

    std::vector<Capture> captures;
    for (std::map<std::string, Capture>::const_iterator it = capturesByPath.begin(); it != capturesByPath.end(); ++it)
        captures.push_back(it->second);
    std::sort(captures.begin(), captures.end(), newerCapture);

    for (std::size_t i = keepCount; i < captures.size(); ++i)
    {
        const Capture& capture = captures[i];
        // Preserve the report while its dump cannot be removed. A report that cannot be
        // deleted is enumerated as an orphan on the next launch and is retried then.
        if (capture.hasDump && !DeleteFileA(capture.dumpPath.c_str()))
        {
            const DWORD error = GetLastError();
            if (error != ERROR_FILE_NOT_FOUND && error != ERROR_PATH_NOT_FOUND)
                continue;
        }
        if (capture.hasReport)
            DeleteFileA((capture.dumpPath + ".txt").c_str());
    }
}
