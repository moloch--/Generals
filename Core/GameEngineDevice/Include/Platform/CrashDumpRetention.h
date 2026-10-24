// GeneralsX @bugfix Codex 24/10/2026 Retain bounded crash captures, including reports whose dump could not be created.
#pragma once

#include <cstddef>

// Directory includes its trailing separator. A dump and its .txt sidecar count as one capture.
void KeepNewestCrashCaptures(const char* directory, char dumpType, std::size_t keepCount);
