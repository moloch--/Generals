// GeneralsX @bugfix Codex 24/10/2026 Cover preloaded DbgHelp symbol sessions and real exception minidumps.
#include "Utility/CppMacros.h"
#include "WWLib/DbgHelpLoader.h"

#include <cstdio>

namespace
{
const DWORD TestExceptionCode = 0xe0424242;
EXCEPTION_RECORD exceptionRecord;
CONTEXT exceptionContext;
EXCEPTION_POINTERS exceptionPointers;
DWORD exceptionThread;
char dumpPath[MAX_PATH];

bool check(bool condition, const char* description)
{
    if (!condition)
        std::fprintf(stderr, "FAIL: %s (last error 0x%08lx)\n", description, GetLastError());
    return condition;
}

LONG captureException(EXCEPTION_POINTERS* pointers)
{
    exceptionRecord = *pointers->ExceptionRecord;
    exceptionContext = *pointers->ContextRecord;
    exceptionPointers.ExceptionRecord = &exceptionRecord;
    exceptionPointers.ContextRecord = &exceptionContext;
    return EXCEPTION_EXECUTE_HANDLER;
}

void raiseTestException()
{
    exceptionThread = GetCurrentThreadId();
    __try
    {
        RaiseException(TestExceptionCode, 0, 0, nullptr);
    }
    __except (captureException(GetExceptionInformation()))
    {
        // The copied context remains valid while the worker captures the process.
        exceptionPointers.ExceptionRecord->ExceptionRecord = nullptr;
    }
}

DWORD WINAPI writeDump(void*)
{
    HANDLE file = CreateFileA(dumpPath, GENERIC_READ | GENERIC_WRITE, 0, nullptr,
        CREATE_ALWAYS, FILE_ATTRIBUTE_NORMAL, nullptr);
    if (!check(file != INVALID_HANDLE_VALUE, "create temporary dump"))
        return 1;

    MINIDUMP_EXCEPTION_INFORMATION exception = {};
    exception.ThreadId = exceptionThread;
    exception.ExceptionPointers = &exceptionPointers;
    const BOOL written = DbgHelpLoader::miniDumpWriteDump(GetCurrentProcess(), GetCurrentProcessId(), file,
        static_cast<MINIDUMP_TYPE>(MiniDumpWithIndirectlyReferencedMemory | MiniDumpScanMemory),
        &exception, nullptr, nullptr);
    const bool succeeded = check(written != FALSE, "write minimal dump with exception context");
    CloseHandle(file);
    return succeeded ? 0 : 1;
}

bool verifyDump()
{
    FILE* file = std::fopen(dumpPath, "rb");
    if (!check(file != nullptr, "open captured dump"))
        return false;
    MINIDUMP_HEADER header = {};
    bool valid = check(std::fread(&header, sizeof(header), 1, file) == 1 &&
        header.Signature == MINIDUMP_SIGNATURE && header.NumberOfStreams != 0,
        "dump contains a valid header and streams");
    bool foundException = false;
    for (ULONG32 i = 0; valid && i < header.NumberOfStreams; ++i)
    {
        MINIDUMP_DIRECTORY directory = {};
        valid = std::fseek(file, header.StreamDirectoryRva + i * sizeof(directory), SEEK_SET) == 0 &&
            std::fread(&directory, sizeof(directory), 1, file) == 1;
        if (valid && directory.StreamType == ExceptionStream)
        {
            MINIDUMP_EXCEPTION_STREAM exception = {};
            valid = std::fseek(file, directory.Location.Rva, SEEK_SET) == 0 &&
                std::fread(&exception, sizeof(exception), 1, file) == 1;
            valid = valid && check(exception.ThreadId == exceptionThread &&
                exception.ExceptionRecord.ExceptionCode == TestExceptionCode &&
                exception.ThreadContext.DataSize >= sizeof(CONTEXT),
                "dump retains the originating exception and thread context");
            foundException = valid;
        }
    }
    std::fclose(file);
    return check(valid && foundException, "exception stream is present and readable");
}
}

int main()
{
    SetLastError(ERROR_SUCCESS);
    const BOOL unavailable = DbgHelpLoader::miniDumpWriteDump(GetCurrentProcess(), GetCurrentProcessId(),
        INVALID_HANDLE_VALUE, MiniDumpNormal, nullptr, nullptr, nullptr);
    if (!check(unavailable == FALSE && GetLastError() == ERROR_PROC_NOT_FOUND,
        "report an unavailable dump entry point without a stale Windows error"))
        return 1;

    // MiniDumper and DX8 guards load the DLL before the first stack report.
    if (!check(DbgHelpLoader::load(), "preload the production DbgHelp wrapper"))
    {
        DbgHelpLoader::unload();
        return 1;
    }
    bool valid = check(DbgHelpLoader::isLoadedFromSystem(), "use the system DbgHelp DLL") &&
        check(DbgHelpLoader::initializeCurrentProcessSymbols(), "initialize symbols after preload");
    const DWORD address = static_cast<DWORD>(reinterpret_cast<DWORD_PTR>(&main));
    const DWORD module = static_cast<DWORD>(reinterpret_cast<DWORD_PTR>(GetModuleHandleA(nullptr)));
    valid = check(DbgHelpLoader::symGetModuleBase(GetCurrentProcess(), address) == module,
        "resolve the executable module after preloading DbgHelp") && valid;
    valid = check(DbgHelpLoader::initializeCurrentProcessSymbols(), "repeat symbol initialization") && valid;
    DbgHelpLoader::unload();
    valid = check(DbgHelpLoader::symGetModuleBase(GetCurrentProcess(), address) == module,
        "retain symbols after the minidumper releases its loader reference") && valid;
    if (!valid)
        return 1;

    SetLastError(ERROR_SUCCESS);
    const BOOL invalidDump = DbgHelpLoader::miniDumpWriteDump(GetCurrentProcess(), GetCurrentProcessId(),
        INVALID_HANDLE_VALUE, MiniDumpNormal, nullptr, nullptr, nullptr);
    if (!check(invalidDump == FALSE && GetLastError() != ERROR_SUCCESS && GetLastError() != ERROR_PROC_NOT_FOUND,
        "retain the actual Windows error for an invalid dump file handle"))
        return 1;

    char tempPath[MAX_PATH];
    if (!check(GetTempPathA(MAX_PATH, tempPath) != 0 &&
        GetTempFileNameA(tempPath, "gxd", 0, dumpPath) != 0, "allocate a temporary dump path"))
        return 1;
    raiseTestException();
    HANDLE worker = CreateThread(nullptr, 0, writeDump, nullptr, 0, nullptr);
    valid = check(worker != nullptr, "start dump worker");
    if (valid)
    {
        DWORD exitCode = 1;
        valid = check(WaitForSingleObject(worker, INFINITE) == WAIT_OBJECT_0 &&
            GetExitCodeThread(worker, &exitCode) && exitCode == 0, "complete dump capture");
        CloseHandle(worker);
    }
    valid = valid && verifyDump();
    DeleteFileA(dumpPath);
    if (valid)
        std::puts("Windows diagnostics: symbols survive preload/unload; exception dump is valid.");
    return valid ? 0 : 1;
}
