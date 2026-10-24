// GeneralsX @bugfix Codex 24/10/2026 Preserve caught exception details without allocating another game-memory string.
#pragma once

#include "Utility/CppMacros.h"
#include "Utility/stdio_adapter.h"
#include "Lib/BaseTypeCore.h"
#include "Common/Errors.h"
#include <exception>

namespace rts
{
namespace diagnostics
{
inline const char* ErrorCodeName(ErrorCode code)
{
    switch (code)
    {
    case ERROR_BUG: return "ERROR_BUG";
    case ERROR_OUT_OF_MEMORY: return "ERROR_OUT_OF_MEMORY";
    case ERROR_BAD_ARG: return "ERROR_BAD_ARG";
    case ERROR_INVALID_FILE_VERSION: return "ERROR_INVALID_FILE_VERSION";
    case ERROR_CORRUPT_FILE_FORMAT: return "ERROR_CORRUPT_FILE_FORMAT";
    case ERROR_BAD_INI: return "ERROR_BAD_INI";
    case ERROR_INVALID_D3D: return "ERROR_INVALID_D3D";
    default: return "ErrorCode";
    }
}

// Call only inside an active catch handler. Rethrowing here retains the original
// exception even after a nested cleanup operation threw and was caught separately.
inline void DescribeCurrentException(char* buffer, size_t capacity, const char* context)
{
    if (buffer == nullptr || capacity == 0)
        return;
    try
    {
        throw;
    }
    catch (ErrorCode code)
    {
        snprintf(buffer, capacity, "Uncaught exception in %s: %s (0x%08X)",
            context, ErrorCodeName(code), static_cast<unsigned int>(code));
    }
    catch (const std::exception& exception)
    {
        const char* detail = exception.what();
        snprintf(buffer, capacity, "Uncaught std::exception in %s: %s",
            context, detail != nullptr ? detail : "no description");
    }
    catch (...)
    {
        snprintf(buffer, capacity, "Uncaught exception in %s: unknown exception type", context);
    }
    buffer[capacity - 1] = '\0';
}
}
}
