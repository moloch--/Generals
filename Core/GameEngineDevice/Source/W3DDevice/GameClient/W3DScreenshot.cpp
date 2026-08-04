/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 TheSuperHackers
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

#include "W3DDevice/GameClient/W3DScreenshot.h"
#include "Common/GlobalData.h"
#include "GameClient/GameText.h"
#include "GameClient/InGameUI.h"
#include "WW3D2/dx8wrapper.h"
#include "WW3D2/surfaceclass.h"
#include <stb_image_write.h>
#include <atomic>
#include <chrono>
#include <cstdio>
#include <cstring>
#include <ctime>
#include <deque>
#include <exception>
#include <filesystem>
#include <limits>
#include <string>
#include <utility>
#include <vector>

#ifdef _WIN32
#include <process.h>
#include <windows.h>
#else
#include <condition_variable>
#include <mutex>
#include <thread>
#endif

namespace
{

// GeneralsX @bugfix OpenAI 30/07/2026 Use one joinable worker and bounded queues instead of
// spawning an unbounded number of detached threads for screenshots.
constexpr std::size_t SCREENSHOT_JOB_CAPACITY = 4;
constexpr std::size_t SCREENSHOT_MESSAGE_CAPACITY = 16;

struct ScreenshotJob
{
	std::vector<unsigned char> pixelData;
	unsigned int width = 0;
	unsigned int height = 0;
	unsigned int pitch = 0;
	bool is16Bit = false;
	std::string userDataDirectory;
	std::string leafname;
	int quality = DEFAULT_JPEG_QUALITY;
	ScreenshotFormat format = SCREENSHOT_JPEG;
};

bool FindAvailableScreenshotPath(
	const std::filesystem::path &directory, const std::string &requestedLeafname,
	std::filesystem::path &pathname, std::string &leafname)
{
	const std::filesystem::path requestedPath = std::filesystem::path(requestedLeafname).filename();
	const std::string stem = requestedPath.stem().string();
	const std::string extension = requestedPath.extension().string();

	for (unsigned int suffix = 0; suffix < 10000; ++suffix)
	{
		if (suffix == 0)
		{
			pathname = directory / requestedPath;
		}
		else
		{
			char suffixText[16];
			std::snprintf(suffixText, sizeof(suffixText), "_%u", suffix);
			pathname = directory / (stem + suffixText + extension);
		}

		std::error_code error;
		const bool exists = std::filesystem::exists(pathname, error);
		if (error)
		{
			DEBUG_LOG(("Failed to inspect screenshot path %s: %s", pathname.string().c_str(), error.message().c_str()));
			return false;
		}
		if (!exists)
		{
			leafname = pathname.filename().string();
			return true;
		}
	}

	DEBUG_LOG(("Failed to find a unique screenshot filename for %s", requestedLeafname.c_str()));
	return false;
}

bool WriteScreenshot(ScreenshotJob &data)
{
	if (data.width == 0 || data.height == 0
		|| data.width > static_cast<unsigned int>((std::numeric_limits<int>::max)() / 3)
		|| data.height > static_cast<unsigned int>((std::numeric_limits<int>::max)()))
	{
		DEBUG_LOG(("Invalid screenshot dimensions %u x %u", data.width, data.height));
		return false;
	}

	if (static_cast<std::size_t>(data.width)
		> (std::numeric_limits<std::size_t>::max)() / data.height)
	{
		DEBUG_LOG(("Screenshot dimensions overflow the pixel count"));
		return false;
	}

	const std::size_t pixelCount = static_cast<std::size_t>(data.width) * data.height;
	if (pixelCount > (std::numeric_limits<std::size_t>::max)() / 3)
	{
		DEBUG_LOG(("Screenshot dimensions overflow the image buffer"));
		return false;
	}

	const std::size_t bytesPerPixel = data.is16Bit ? 2 : 4;
	const std::size_t minimumPitch = static_cast<std::size_t>(data.width) * bytesPerPixel;
	if (data.pitch < minimumPitch
		|| static_cast<std::size_t>(data.pitch) > (std::numeric_limits<std::size_t>::max)() / data.height
		|| data.pixelData.size() < static_cast<std::size_t>(data.pitch) * data.height)
	{
		DEBUG_LOG(("Screenshot pixel buffer is smaller than its declared dimensions"));
		return false;
	}

	// TheSuperHackers @feature bobtista 08/07/2026 Save screenshots into a Screenshots subfolder
	// to keep the user data root folder tidy.
	std::filesystem::path screenshotDirectory(data.userDataDirectory);
	screenshotDirectory /= "Screenshots";

	std::error_code error;
	std::filesystem::create_directories(screenshotDirectory, error);
	if (error)
	{
		DEBUG_LOG(("Failed to create screenshot directory %s: %s",
			screenshotDirectory.string().c_str(), error.message().c_str()));
		return false;
	}

	std::filesystem::path pathname;
	if (!FindAvailableScreenshotPath(screenshotDirectory, data.leafname, pathname, data.leafname))
	{
		return false;
	}

	// Convert to R8G8B8 for stb_image_write.
	std::vector<unsigned char> image(3 * pixelCount);

	if (!data.is16Bit)
	{
		// Convert A8R8G8B8/X8R8G8B8 to R8G8B8.
		for (unsigned int y = 0; y < data.height; ++y)
		{
			const unsigned int *srcLine =
				reinterpret_cast<const unsigned int *>(data.pixelData.data() + static_cast<std::size_t>(y) * data.pitch);
			for (unsigned int x = 0; x < data.width; ++x)
			{
				const unsigned int argb = srcLine[x];
				const std::size_t index = 3 * (static_cast<std::size_t>(x) + static_cast<std::size_t>(y) * data.width);
				image[index + 0] = static_cast<unsigned char>(argb >> 16); // r
				image[index + 1] = static_cast<unsigned char>(argb >> 8);  // g
				image[index + 2] = static_cast<unsigned char>(argb >> 0);  // b
			}
		}
	}
	else
	{
		// Convert R5G6B5 to R8G8B8.
		for (unsigned int y = 0; y < data.height; ++y)
		{
			const unsigned short *srcLine =
				reinterpret_cast<const unsigned short *>(data.pixelData.data() + static_cast<std::size_t>(y) * data.pitch);
			for (unsigned int x = 0; x < data.width; ++x)
			{
				const unsigned short rgb = srcLine[x];
				const std::size_t index = 3 * (static_cast<std::size_t>(x) + static_cast<std::size_t>(y) * data.width);
				image[index + 0] = static_cast<unsigned char>((rgb & 0xF800) >> 8); // r
				image[index + 1] = static_cast<unsigned char>((rgb & 0x07E0) >> 3); // g
				image[index + 2] = static_cast<unsigned char>((rgb & 0x001F) << 3); // b
			}
		}
	}

	const std::string pathnameString = pathname.string();
	int success = 0;
	switch (data.format)
	{
		case SCREENSHOT_JPEG:
			success = stbi_write_jpg(
				pathnameString.c_str(), static_cast<int>(data.width), static_cast<int>(data.height), 3, image.data(), data.quality);
			break;
		case SCREENSHOT_PNG:
			success = stbi_write_png(pathnameString.c_str(), static_cast<int>(data.width), static_cast<int>(data.height), 3,
				image.data(), static_cast<int>(data.width * 3));
			break;
		default:
			break;
	}

	if (!success)
	{
		DEBUG_LOG(("Failed to write screenshot %s", pathnameString.c_str()));
	}
	return success != 0;
}

#ifdef _WIN32

class NativeCriticalSectionLock
{
public:
	explicit NativeCriticalSectionLock(CRITICAL_SECTION &criticalSection)
		: m_criticalSection(criticalSection)
	{
		EnterCriticalSection(&m_criticalSection);
	}

	~NativeCriticalSectionLock()
	{
		LeaveCriticalSection(&m_criticalSection);
	}

private:
	CRITICAL_SECTION &m_criticalSection;
};

class ScreenshotWorker
{
public:
	ScreenshotWorker()
	{
		InitializeCriticalSection(&m_lock);
		m_jobReady = CreateEvent(nullptr, FALSE, FALSE, nullptr);
	}

	~ScreenshotWorker()
	{
		shutdown();
		if (m_jobReady != nullptr)
		{
			CloseHandle(m_jobReady);
		}
		DeleteCriticalSection(&m_lock);
	}

	bool canAcceptJob()
	{
		NativeCriticalSectionLock lock(m_lock);
		return m_jobReady != nullptr && !m_stopping && m_outstandingJobs < SCREENSHOT_JOB_CAPACITY;
	}

	bool enqueue(ScreenshotJob &&job)
	{
		NativeCriticalSectionLock lock(m_lock);
		if (m_jobReady == nullptr || m_stopping || m_outstandingJobs >= SCREENSHOT_JOB_CAPACITY)
		{
			return false;
		}

		if (m_thread == nullptr)
		{
			const uintptr_t thread = _beginthreadex(nullptr, 0, &ScreenshotWorker::threadEntry, this, 0, nullptr);
			if (thread == 0)
			{
				DEBUG_LOG(("Failed to start screenshot worker"));
				return false;
			}
			m_thread = reinterpret_cast<HANDLE>(thread);
		}

		try
		{
			m_jobs.emplace_back(std::move(job));
			++m_outstandingJobs;
		}
		catch (const std::exception &exception)
		{
			// GeneralsX @build Codex 04/08/2026 Keep release builds warning-clean when debug logging compiles out.
			(void) exception;
			DEBUG_LOG(("Failed to queue screenshot: %s", exception.what()));
			return false;
		}
		catch (...)
		{
			DEBUG_LOG(("Failed to queue screenshot"));
			return false;
		}

		if (!SetEvent(m_jobReady))
		{
			m_jobs.pop_back();
			--m_outstandingJobs;
			DEBUG_LOG(("Failed to signal screenshot worker"));
			return false;
		}
		return true;
	}

	std::deque<std::string> takeWrittenLeafnames()
	{
		NativeCriticalSectionLock lock(m_lock);
		std::deque<std::string> messages;
		m_writtenLeafnames.swap(messages);
		return messages;
	}

private:
	static unsigned __stdcall threadEntry(void *context)
	{
		static_cast<ScreenshotWorker *>(context)->run();
		return 0;
	}

	void shutdown()
	{
		HANDLE thread = nullptr;
		{
			NativeCriticalSectionLock lock(m_lock);
			m_stopping = true;
			thread = m_thread;
			if (thread != nullptr)
			{
				SetEvent(m_jobReady);
			}
		}

		if (thread != nullptr)
		{
			WaitForSingleObject(thread, INFINITE);
			CloseHandle(thread);
			m_thread = nullptr;
		}
	}

	void run()
	{
		for (;;)
		{
			const DWORD waitResult = WaitForSingleObject(m_jobReady, INFINITE);
			if (waitResult != WAIT_OBJECT_0)
			{
				DEBUG_LOG(("Screenshot worker wait failed with error %lu", GetLastError()));
				NativeCriticalSectionLock lock(m_lock);
				m_stopping = true;
				return;
			}

			for (;;)
			{
				ScreenshotJob job;
				{
					NativeCriticalSectionLock lock(m_lock);
					if (m_jobs.empty())
					{
						if (m_stopping)
						{
							return;
						}
						break;
					}
					job = std::move(m_jobs.front());
					m_jobs.pop_front();
				}

				process(job);
			}
		}
	}

	void process(ScreenshotJob &job)
	{
		bool success = false;
		try
		{
			success = WriteScreenshot(job);
		}
		catch (const std::exception &exception)
		{
			// GeneralsX @build Codex 04/08/2026 Keep release builds warning-clean when debug logging compiles out.
			(void) exception;
			DEBUG_LOG(("Screenshot worker failed: %s", exception.what()));
		}
		catch (...)
		{
			DEBUG_LOG(("Screenshot worker failed"));
		}

		NativeCriticalSectionLock lock(m_lock);
		if (success)
		{
			try
			{
				if (m_writtenLeafnames.size() == SCREENSHOT_MESSAGE_CAPACITY)
				{
					m_writtenLeafnames.pop_front();
				}
				m_writtenLeafnames.emplace_back(std::move(job.leafname));
			}
			catch (...)
			{
				DEBUG_LOG(("Failed to queue screenshot completion message"));
			}
		}
		--m_outstandingJobs;
	}

	CRITICAL_SECTION m_lock;
	HANDLE m_jobReady = nullptr;
	HANDLE m_thread = nullptr;
	std::deque<ScreenshotJob> m_jobs;
	std::deque<std::string> m_writtenLeafnames;
	std::size_t m_outstandingJobs = 0;
	bool m_stopping = false;
};

#else

class ScreenshotWorker
{
public:
	~ScreenshotWorker()
	{
		shutdown();
	}

	bool canAcceptJob()
	{
		std::lock_guard<std::mutex> lock(m_jobMutex);
		return !m_stopping && m_outstandingJobs < SCREENSHOT_JOB_CAPACITY;
	}

	bool enqueue(ScreenshotJob &&job)
	{
		std::unique_lock<std::mutex> lock(m_jobMutex);
		if (m_stopping || m_outstandingJobs >= SCREENSHOT_JOB_CAPACITY)
		{
			return false;
		}

		try
		{
			if (!m_thread.joinable())
			{
				m_thread = std::thread(&ScreenshotWorker::run, this);
			}
			m_jobs.emplace_back(std::move(job));
			++m_outstandingJobs;
		}
		catch (const std::exception &exception)
		{
			DEBUG_LOG(("Failed to queue screenshot: %s", exception.what()));
			return false;
		}
		catch (...)
		{
			DEBUG_LOG(("Failed to queue screenshot"));
			return false;
		}

		lock.unlock();
		m_jobReady.notify_one();
		return true;
	}

	std::deque<std::string> takeWrittenLeafnames()
	{
		std::lock_guard<std::mutex> lock(m_messageMutex);
		std::deque<std::string> messages;
		m_writtenLeafnames.swap(messages);
		return messages;
	}

private:
	void shutdown()
	{
		{
			std::lock_guard<std::mutex> lock(m_jobMutex);
			m_stopping = true;
		}
		m_jobReady.notify_one();
		if (m_thread.joinable())
		{
			m_thread.join();
		}
	}

	void run()
	{
		for (;;)
		{
			ScreenshotJob job;
			{
				std::unique_lock<std::mutex> lock(m_jobMutex);
				m_jobReady.wait(lock, [this] { return m_stopping || !m_jobs.empty(); });
				if (m_stopping && m_jobs.empty())
				{
					return;
				}
				job = std::move(m_jobs.front());
				m_jobs.pop_front();
			}

			bool success = false;
			try
			{
				success = WriteScreenshot(job);
			}
			catch (const std::exception &exception)
			{
				DEBUG_LOG(("Screenshot worker failed: %s", exception.what()));
			}
			catch (...)
			{
				DEBUG_LOG(("Screenshot worker failed"));
			}

			if (success)
			{
				std::lock_guard<std::mutex> lock(m_messageMutex);
				try
				{
					if (m_writtenLeafnames.size() == SCREENSHOT_MESSAGE_CAPACITY)
					{
						m_writtenLeafnames.pop_front();
					}
					m_writtenLeafnames.emplace_back(std::move(job.leafname));
				}
				catch (...)
				{
					DEBUG_LOG(("Failed to queue screenshot completion message"));
				}
			}

			{
				std::lock_guard<std::mutex> lock(m_jobMutex);
				--m_outstandingJobs;
			}
		}
	}

	std::mutex m_jobMutex;
	std::condition_variable m_jobReady;
	std::deque<ScreenshotJob> m_jobs;
	std::thread m_thread;
	std::size_t m_outstandingJobs = 0;
	bool m_stopping = false;

	std::mutex m_messageMutex;
	std::deque<std::string> m_writtenLeafnames;
};

#endif

ScreenshotWorker &GetScreenshotWorker()
{
	static ScreenshotWorker worker;
	return worker;
}

std::string MakeScreenshotLeafname(const char *extension)
{
	static std::atomic<unsigned long long> sequence { 0 };

	const auto now = std::chrono::system_clock::now();
	const std::time_t time = std::chrono::system_clock::to_time_t(now);
	const long long milliseconds =
		std::chrono::duration_cast<std::chrono::milliseconds>(now.time_since_epoch()).count() % 1000;

	std::tm localTime {};
#ifdef _WIN32
	if (localtime_s(&localTime, &time) != 0)
	{
		localTime = {};
	}
#else
	if (localtime_r(&time, &localTime) == nullptr)
	{
		localTime = {};
	}
#endif

	char leafname[_MAX_FNAME];
	std::snprintf(leafname, sizeof(leafname), "sshot_%04d%02d%02d_%02d%02d%02d_%03lld_%06llu.%s",
		localTime.tm_year + 1900, localTime.tm_mon + 1, localTime.tm_mday,
		localTime.tm_hour, localTime.tm_min, localTime.tm_sec, milliseconds,
		sequence.fetch_add(1, std::memory_order_relaxed), extension);
	return leafname;
}

} // namespace

void W3D_UpdateScreenshotMessages()
{
	std::deque<std::string> leafnames = GetScreenshotWorker().takeWrittenLeafnames();
	if (TheInGameUI == nullptr || TheGameText == nullptr)
	{
		return;
	}

	for (const std::string &leafname : leafnames)
	{
		UnicodeString ufileName;
		ufileName.translate(leafname.c_str());
		TheInGameUI->message(TheGameText->fetch("GUI:ScreenCapture"), ufileName.str());
	}
}

void W3D_TakeCompressedScreenshot(ScreenshotFormat format, Int jpegQuality)
{
	static constexpr const char *const ScreenshotFormatExtensions[] = { "jpg", "png" };
	static_assert(ARRAY_SIZE(ScreenshotFormatExtensions) == SCREENSHOT_FORMAT_COUNT, "Incorrect array size");

	if (format < SCREENSHOT_JPEG || format >= SCREENSHOT_FORMAT_COUNT)
	{
		DEBUG_LOG(("Invalid screenshot format %d", static_cast<int>(format)));
		return;
	}
	if (TheGlobalData == nullptr || TheGlobalData->getPath_UserData().isEmpty())
	{
		DEBUG_LOG(("Cannot take screenshot without a user data directory"));
		return;
	}

	ScreenshotWorker &worker = GetScreenshotWorker();
	if (!worker.canAcceptJob())
	{
		DEBUG_LOG(("Screenshot queue is full; dropping capture"));
		return;
	}

	// The filename is created here so the timestamp matches the capture time. The sequence
	// suffix and the worker's filesystem check prevent rapid captures from overwriting files.
	const std::string leafname = MakeScreenshotLeafname(ScreenshotFormatExtensions[format]);

	// TheSuperHackers @bugfix xezon 21/05/2025 Get the back buffer and create a copy of the surface.
	// Originally this code took the front buffer and tried to lock it. This does not work when the
	// render view clips outside the desktop boundaries. It crashed the game.
	SurfaceClass *surface = DX8Wrapper::_Get_DX8_Back_Buffer();
	if (surface == nullptr || surface->Peek_D3D_Surface() == nullptr)
	{
		if (surface != nullptr)
		{
			surface->Release_Ref();
		}
		DEBUG_LOG(("Failed to get the back buffer for screenshot"));
		return;
	}

	SurfaceClass::SurfaceDescription surfaceDesc;
	surface->Get_Description(surfaceDesc);

	// TheSuperHackers @bugfix bobtista 08/07/2026 Support the 16 bit back buffer format that the
	// game uses when running in 16 bit color mode. Reading it with the 32 bit stride read garbage.
	const bool is32Bit = surfaceDesc.Format == WW3D_FORMAT_A8R8G8B8 || surfaceDesc.Format == WW3D_FORMAT_X8R8G8B8;
	const bool is16Bit = surfaceDesc.Format == WW3D_FORMAT_R5G6B5;

	if ((!is32Bit && !is16Bit) || surfaceDesc.Width == 0 || surfaceDesc.Height == 0)
	{
		DEBUG_LOG(("Screenshot does not support back buffer format %d", static_cast<int>(surfaceDesc.Format)));
		surface->Release_Ref();
		return;
	}

	SurfaceClass *surfaceCopy =
		NEW_REF(SurfaceClass, (surfaceDesc.Width, surfaceDesc.Height, surfaceDesc.Format));
	if (surfaceCopy == nullptr || surfaceCopy->Peek_D3D_Surface() == nullptr)
	{
		surface->Release_Ref();
		if (surfaceCopy != nullptr)
		{
			surfaceCopy->Release_Ref();
		}
		DEBUG_LOG(("Failed to allocate the screenshot surface"));
		return;
	}

	DX8Wrapper::_Copy_DX8_Rects(
		surface->Peek_D3D_Surface(), nullptr, 0, surfaceCopy->Peek_D3D_Surface(), nullptr);
	surface->Release_Ref();

	struct LockedRect
	{
		int pitch;
		void *bits;
	} lockedRect {};

	lockedRect.bits = surfaceCopy->Lock(&lockedRect.pitch);
	const std::size_t bytesPerPixel = is16Bit ? 2 : 4;
	if (surfaceDesc.Width > (std::numeric_limits<std::size_t>::max)() / bytesPerPixel)
	{
		if (lockedRect.bits != nullptr)
		{
			surfaceCopy->Unlock();
		}
		surfaceCopy->Release_Ref();
		DEBUG_LOG(("Screenshot width overflows its row size"));
		return;
	}
	const std::size_t minimumPitch = static_cast<std::size_t>(surfaceDesc.Width) * bytesPerPixel;
	if (lockedRect.bits == nullptr || lockedRect.pitch <= 0
		|| static_cast<std::size_t>(lockedRect.pitch) < minimumPitch
		|| static_cast<std::size_t>(lockedRect.pitch)
			> (std::numeric_limits<std::size_t>::max)() / surfaceDesc.Height)
	{
		if (lockedRect.bits != nullptr)
		{
			surfaceCopy->Unlock();
		}
		surfaceCopy->Release_Ref();
		DEBUG_LOG(("Failed to lock a valid screenshot surface"));
		return;
	}

	ScreenshotJob job;
	job.width = surfaceDesc.Width;
	job.height = surfaceDesc.Height;
	job.pitch = static_cast<unsigned int>(lockedRect.pitch);
	job.is16Bit = is16Bit;
	job.quality = jpegQuality < 1 ? 1 : (jpegQuality > 100 ? 100 : jpegQuality);
	job.format = format;

	// Copy the locked surface with a single memcpy, including any row padding. The pixel
	// conversion and all file operations are done on the screenshot worker to keep the
	// main thread cheap.
	const std::size_t pixelDataSize = static_cast<std::size_t>(job.pitch) * job.height;
	try
	{
		job.userDataDirectory = TheGlobalData->getPath_UserData().str();
		job.leafname = leafname;
		job.pixelData.resize(pixelDataSize);
		std::memcpy(job.pixelData.data(), lockedRect.bits, pixelDataSize);
	}
	catch (const std::exception &exception)
	{
		surfaceCopy->Unlock();
		surfaceCopy->Release_Ref();
		// GeneralsX @build Codex 04/08/2026 Keep release builds warning-clean when debug logging compiles out.
		(void) exception;
		DEBUG_LOG(("Failed to copy screenshot pixels: %s", exception.what()));
		return;
	}
	catch (...)
	{
		surfaceCopy->Unlock();
		surfaceCopy->Release_Ref();
		DEBUG_LOG(("Failed to copy screenshot pixels"));
		return;
	}

	surfaceCopy->Unlock();
	surfaceCopy->Release_Ref();

	if (!worker.enqueue(std::move(job)))
	{
		DEBUG_LOG(("Screenshot queue is full or unavailable; dropping capture"));
	}
}
