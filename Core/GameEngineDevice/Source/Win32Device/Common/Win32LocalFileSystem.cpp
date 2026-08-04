/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
**
**	This program is distributed in the hope that it will be useful,
**	but WITHOUT ANY WARRANTY; without even the implied warranty of
**	MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
**	GNU General Public License for more details.
**
**	You should have received a copy of the GNU General Public License
**	along with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

////////////////////////////////////////////////////////////////////////////////
//																																						//
//  (c) 2001-2003 Electronic Arts Inc.																				//
//																																						//
////////////////////////////////////////////////////////////////////////////////

///////// Win32LocalFileSystem.cpp /////////////////////////
// Bryan Cleveland, August 2002
////////////////////////////////////////////////////////////

#include <windows.h>
#include "Common/AsciiString.h"
#include "Common/GameMemory.h"
#include "Common/PerfTimer.h"
#include "Win32Device/Common/Win32LocalFileSystem.h"
#include "Win32Device/Common/Win32LocalFile.h"
#include <cstdlib>
#include <filesystem>
#include <io.h>
#include <string>

namespace {

// GeneralsX @feature Codex 04/08/2026 Keep authenticated SFX reads separate from writable process state.
static std::filesystem::path s_assetRootPath;

static std::filesystem::path getSFXRuntimeStateFileSystemPath()
{
	const char *statePath = std::getenv("GENERALSX_SFX_RUNTIME_STATE");
	if (statePath == nullptr || statePath[0] == '\0') {
		return std::filesystem::path();
	}

	std::filesystem::path path(statePath);
	if (!path.is_absolute()) {
		return std::filesystem::path();
	}
	return path.lexically_normal();
}

static Bool isSafeSFXRelativePath(const std::filesystem::path& path)
{
	if (path.empty() || path.is_absolute() || path.has_root_name() || path.has_root_directory()) {
		return FALSE;
	}
	if (path.generic_string().find(':') != std::string::npos) {
		return FALSE;
	}
	for (const auto& component : path.lexically_normal()) {
		if (component == "..") {
			return FALSE;
		}
	}
	return TRUE;
}

static Bool requestsWriteAccess(Int access)
{
	return (access & (File::WRITE | File::APPEND | File::CREATE | File::TRUNCATE | File::ONLYNEW)) != 0;
}

static std::filesystem::path resolveSFXLocalPath(const Char *filename, Int access)
{
	std::filesystem::path path(filename);
	const std::filesystem::path runtimeStatePath = getSFXRuntimeStateFileSystemPath();
	if (runtimeStatePath.empty() || path.is_absolute()) {
		return path;
	}
	if (!isSafeSFXRelativePath(path)) {
		return std::filesystem::path();
	}
	if (requestsWriteAccess(access)) {
		return runtimeStatePath / path.lexically_normal();
	}
	if (s_assetRootPath.empty()) {
		return std::filesystem::path();
	}
	return s_assetRootPath / path.lexically_normal();
}

static void appendDirectorySeparator(AsciiString& path)
{
	if (path.isEmpty()) {
		return;
	}
	const Char last = path.getCharAt(path.getLength() - 1);
	if (last != '\\' && last != '/') {
		path.concat('\\');
	}
}

}

AsciiString getWin32SFXRuntimeStatePath()
{
	const std::filesystem::path runtimeStatePath = getSFXRuntimeStateFileSystemPath();
	return runtimeStatePath.empty() ? AsciiString::TheEmptyString : AsciiString(runtimeStatePath.string().c_str());
}

AsciiString resolveWin32SFXAssetReadPath(const AsciiString& path, const AsciiString& assetRootPath)
{
	const std::filesystem::path requested(path.str());
	if (getSFXRuntimeStateFileSystemPath().empty() || requested.is_absolute()) {
		return path;
	}

	const std::filesystem::path assetRoot(assetRootPath.str());
	if (assetRoot.empty() || !assetRoot.is_absolute() || !isSafeSFXRelativePath(requested)) {
		return AsciiString::TheEmptyString;
	}
	return AsciiString((assetRoot.lexically_normal() / requested.lexically_normal()).string().c_str());
}

Win32LocalFileSystem::Win32LocalFileSystem() : LocalFileSystem()
{
}

Win32LocalFileSystem::~Win32LocalFileSystem() {
}

//DECLARE_PERF_TIMER(Win32LocalFileSystem_openFile)
File * Win32LocalFileSystem::openFile(const Char *filename, Int access, size_t bufferSize)
{
	//USE_PERF_TIMER(Win32LocalFileSystem_openFile)

	// sanity check
	if (strlen(filename) <= 0) {
		return nullptr;
	}

	const std::filesystem::path resolvedPath = resolveSFXLocalPath(filename, access);
	if (resolvedPath.empty()) {
		return nullptr;
	}
	const std::string resolvedFilename = resolvedPath.string();

	if (requestsWriteAccess(access)) {
		// GeneralsX @bugfix Codex 04/08/2026 Create writable SFX parents beneath runtime state, including absolute state roots.
		const std::filesystem::path directory = resolvedPath.parent_path();
		std::error_code ec;
		if (!directory.empty() && !std::filesystem::exists(directory, ec)) {
			if (!std::filesystem::create_directories(directory, ec) || ec) {
				return nullptr;
			}
		}
	}

	// TheSuperHackers @fix Mauller 21/04/2025 Create new file handle when necessary to prevent memory leak
	Win32LocalFile *file = newInstance( Win32LocalFile );

	if (file->open(resolvedFilename.c_str(), access, bufferSize) == FALSE) {
		deleteInstance(file);
		file = nullptr;
	} else {
		file->deleteOnClose();
	}

// this will also need to play nice with the STREAMING type that I added, if we ever enable this

// srj sez: this speeds up INI loading, but makes BIG files unusable.
// don't enable it without further tweaking.
//
// unless you like running really slowly.
//	if (!(access&File::WRITE)) {
//		// Return a ramfile.
//		RAMFile *ramFile = newInstance( RAMFile );
//		if (ramFile->open(file)) {
//			file->close(); // is deleteonclose, so should delete.
//			ramFile->deleteOnClose();
//			return ramFile;
//		}	else {
//			ramFile->close();
//			deleteInstance(ramFile);
//		}
//	}

	return file;
}

void Win32LocalFileSystem::update()
{
}

void Win32LocalFileSystem::init()
{
}

void Win32LocalFileSystem::reset()
{
}

//DECLARE_PERF_TIMER(Win32LocalFileSystem_doesFileExist)
Bool Win32LocalFileSystem::doesFileExist(const Char *filename) const
{
	//USE_PERF_TIMER(Win32LocalFileSystem_doesFileExist)
	const std::filesystem::path resolvedPath = resolveSFXLocalPath(filename, File::READ);
	if (!resolvedPath.empty() && _access(resolvedPath.string().c_str(), 0) == 0) {
		return TRUE;
	}
	return FALSE;
}

void Win32LocalFileSystem::getFileListInDirectory(const AsciiString& currentDirectory, const AsciiString& originalDirectory, const AsciiString& searchName, FilenameList & filenameList, Bool searchSubdirectories) const
{
	HANDLE fileHandle = nullptr;
	WIN32_FIND_DATA findData;

	AsciiString logicalDirectory;
	logicalDirectory = originalDirectory;
	logicalDirectory.concat(currentDirectory);
	AsciiString directoryToResolve = logicalDirectory;
	if (directoryToResolve.isEmpty()) {
		directoryToResolve = ".";
	}
	AsciiString physicalDirectory = resolveAssetReadPath(directoryToResolve);
	if (physicalDirectory.isEmpty()) {
		return;
	}

	AsciiString asciisearch = physicalDirectory;
	appendDirectorySeparator(asciisearch);
	asciisearch.concat(searchName);

	Bool done = FALSE;

	fileHandle = FindFirstFile(asciisearch.str(), &findData);
	done = (fileHandle == INVALID_HANDLE_VALUE);

	while (!done)	{
		if (!(findData.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) &&
				(strcmp(findData.cFileName, ".") != 0 && strcmp(findData.cFileName, "..") != 0)) {
			// if we haven't already, add this filename to the list.
				// a stl set should only allow one copy of each filename
				AsciiString newFilename;
				newFilename = logicalDirectory;
				appendDirectorySeparator(newFilename);
				newFilename.concat(findData.cFileName);
				if (filenameList.find(newFilename) == filenameList.end()) {
					filenameList.insert(newFilename);
				}
		}

		done = (FindNextFile(fileHandle, &findData) == 0);
	}
	FindClose(fileHandle);

	if (searchSubdirectories) {
		AsciiString subdirsearch = physicalDirectory;
		appendDirectorySeparator(subdirsearch);
		subdirsearch.concat("*.");
		fileHandle = FindFirstFile(subdirsearch.str(), &findData);
		done = fileHandle == INVALID_HANDLE_VALUE;

		while (!done) {
			if ((findData.dwFileAttributes & FILE_ATTRIBUTE_DIRECTORY) &&
					(strcmp(findData.cFileName, ".") != 0 && strcmp(findData.cFileName, "..") != 0)) {

					AsciiString tempsearchstr;
					tempsearchstr = currentDirectory;
					appendDirectorySeparator(tempsearchstr);
					tempsearchstr.concat(findData.cFileName);
					tempsearchstr.concat('\\');

					// recursively add files in subdirectories if required.
					getFileListInDirectory(tempsearchstr, originalDirectory, searchName, filenameList, searchSubdirectories);
			}

			done = (FindNextFile(fileHandle, &findData) == 0);
		}

		FindClose(fileHandle);
	}

}

Bool Win32LocalFileSystem::getFileInfo(const AsciiString& filename, FileInfo *fileInfo) const
{
	WIN32_FIND_DATA findData;
	HANDLE findHandle = nullptr;
	const std::filesystem::path resolvedPath = resolveSFXLocalPath(filename.str(), File::READ);
	if (resolvedPath.empty()) {
		return FALSE;
	}
	findHandle = FindFirstFile(resolvedPath.string().c_str(), &findData);

	if (findHandle == INVALID_HANDLE_VALUE) {
		return FALSE;
	}

	fileInfo->timestampHigh = findData.ftLastWriteTime.dwHighDateTime;
	fileInfo->timestampLow = findData.ftLastWriteTime.dwLowDateTime;
	fileInfo->sizeHigh = findData.nFileSizeHigh;
	fileInfo->sizeLow = findData.nFileSizeLow;

	FindClose(findHandle);

	return TRUE;
}

Bool Win32LocalFileSystem::createDirectory(AsciiString directory)
{
	if ((!directory.isEmpty()) && (directory.getLength() < _MAX_DIR)) {
		const std::filesystem::path resolvedPath = resolveSFXLocalPath(directory.str(), File::WRITE | File::CREATE);
		if (resolvedPath.empty()) {
			return FALSE;
		}
		std::error_code ec;
		return std::filesystem::create_directory(resolvedPath, ec) && !ec;
	}
	return FALSE;
}

AsciiString Win32LocalFileSystem::normalizePath(const AsciiString& filePath) const
{
	DWORD retval = GetFullPathNameA(filePath.str(), 0, nullptr, nullptr);
	if (retval == 0)
	{
		DEBUG_LOG(("Unable to determine buffer size for normalized file path. Error=(%u).", GetLastError()));
		return AsciiString::TheEmptyString;
	}

	AsciiString normalizedFilePath;
	retval = GetFullPathNameA(filePath.str(), retval, normalizedFilePath.getBufferForRead(retval - 1), nullptr);
	if (retval == 0)
	{
		DEBUG_LOG(("Unable to normalize file path '%s'. Error=(%u).", filePath.str(), GetLastError()));
		return AsciiString::TheEmptyString;
	}

	return normalizedFilePath;
}

void Win32LocalFileSystem::setAssetRootPath(const AsciiString& path)
{
	std::filesystem::path candidate(path.str());
	if (candidate.empty() || (!getSFXRuntimeStateFileSystemPath().empty() && !candidate.is_absolute())) {
		s_assetRootPath.clear();
		return;
	}
	s_assetRootPath = candidate.lexically_normal();
}

AsciiString Win32LocalFileSystem::resolveAssetReadPath(const AsciiString& path) const
{
	return resolveWin32SFXAssetReadPath(path, AsciiString(s_assetRootPath.string().c_str()));
}
