// GeneralsX @bugfix Codex 24/10/2026 Keep the original exception actionable after a failed replay cleanup.
#include "Common/ExceptionDiagnostics.h"

#include <cstdio>
#include <cstring>
#include <stdexcept>

namespace
{
int failures = 0;

void check(bool condition, const char* description)
{
    if (!condition)
    {
        std::fprintf(stderr, "FAIL: %s\n", description);
        ++failures;
    }
}

template<class Exception>
void describe(const Exception& exception, char* buffer, size_t capacity)
{
    try
    {
        throw exception;
    }
    catch (...)
    {
        rts::diagnostics::DescribeCurrentException(buffer, capacity, "GameEngine::update");
    }
}
}

int main()
{
    char reason[512];
    describe(ERROR_OUT_OF_MEMORY, reason, sizeof(reason));
    check(std::strstr(reason, "ERROR_OUT_OF_MEMORY (0xDEAD0002)") != nullptr,
        "name and numeric code identify engine allocation failure");
    describe(static_cast<ErrorCode>(0xdeadffff), reason, sizeof(reason));
    check(std::strstr(reason, "ErrorCode (0xDEADFFFF)") != nullptr, "unknown engine codes retain their value");
    describe(std::runtime_error("invalid command payload"), reason, sizeof(reason));
    check(std::strstr(reason, "std::exception") != nullptr &&
        std::strstr(reason, "invalid command payload") != nullptr, "standard exception preserves what()");
    describe(std::runtime_error("100% complete; %n %s"), reason, sizeof(reason));
    check(std::strstr(reason, "100% complete; %n %s") != nullptr, "exception text is data, not a format string");
    describe(17, reason, sizeof(reason));
    check(std::strstr(reason, "unknown exception type") != nullptr, "unrecognized exception remains reportable");

    bool cleanupFailed = false;
    try
    {
        throw ERROR_OUT_OF_MEMORY;
    }
    catch (...)
    {
        try
        {
            throw std::runtime_error("secondary cleanup failure");
        }
        catch (...)
        {
            cleanupFailed = true;
        }
        rts::diagnostics::DescribeCurrentException(reason, sizeof(reason), "GameEngine::update");
    }
    check(cleanupFailed && std::strstr(reason, "ERROR_OUT_OF_MEMORY") != nullptr &&
        std::strstr(reason, "secondary cleanup failure") == nullptr,
        "failed cleanup does not replace the original exception");

    char bounded[9] = {};
    bounded[8] = 'Z';
    describe(std::runtime_error("long description"), bounded, 8);
    check(bounded[7] == '\0' && bounded[8] == 'Z', "truncation terminates without overwriting the buffer");
    bounded[0] = 'Y';
    describe(ERROR_BUG, bounded, 0);
    check(bounded[0] == 'Y', "zero capacity leaves the buffer untouched");
    describe(ERROR_BUG, nullptr, 0);

    if (failures == 0)
        std::puts("Exception diagnostics: original details survive cleanup; formatting is bounded.");
    return failures == 0 ? 0 : 1;
}
