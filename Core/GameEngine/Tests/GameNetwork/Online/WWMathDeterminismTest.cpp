/*
**	Command & Conquer Generals Zero Hour(tm)
**	Copyright 2025 Electronic Arts Inc.
**
**	This program is free software: you can redistribute it and/or modify
**	it under the terms of the GNU General Public License as published by
**	the Free Software Foundation, either version 3 of the License, or
**	(at your option) any later version.
*/

#include "WWMath/wwmath.h"
#include "WWLib/Point.h"

#include <cstdint>
#include <cstring>
#include <iomanip>
#include <iostream>
#include <limits>
#include <type_traits>

#if !defined(USE_DETERMINISTIC_MATH)
#error "WWMath determinism tests require USE_DETERMINISTIC_MATH"
#endif

namespace
{

static_assert(sizeof(float) == sizeof(std::uint32_t));
static_assert(std::numeric_limits<float>::is_iec559);
static_assert(std::is_same_v<decltype(WWMath::SqrtOrigin(1.0f)), float>);
static_assert(std::is_same_v<decltype(WWMath::SqrtOrigin(1.0)), double>);

float FloatFromBits(std::uint32_t bits)
{
	float value = 0.0f;
	std::memcpy(&value, &bits, sizeof(value));
	return value;
}

std::uint32_t FloatBits(float value)
{
	std::uint32_t bits = 0;
	std::memcpy(&bits, &value, sizeof(bits));
	return bits;
}

bool ExpectBits(const char *name, float actual, std::uint32_t expected)
{
	const std::uint32_t actualBits = FloatBits(actual);
	if (actualBits == expected)
		return true;

	std::cerr << "FAIL: " << name << " expected 0x"
		<< std::hex << std::setfill('0') << std::setw(8) << expected
		<< ", got 0x" << std::setw(8) << actualBits << '\n';
	return false;
}

} // namespace

// GeneralsX @test OpenAI 02/08/2026 Detect CRT, x87, or architecture-specific fallback in simulation math.
int main()
{
	const float forty = FloatFromBits(UINT32_C(0x42200000));
	const float eightyOne = FloatFromBits(UINT32_C(0x42a20000));
	const float two = FloatFromBits(UINT32_C(0x40000000));
	const float positiveZero = FloatFromBits(UINT32_C(0x00000000));
	const float negativeOne = FloatFromBits(UINT32_C(0xbf800000));
	const float negativeOnePointEight = FloatFromBits(UINT32_C(0xbfe66666));
	const float positiveOne = FloatFromBits(UINT32_C(0x3f800000));

	bool passed = true;
	passed &= ExpectBits("WWMath::Sin", WWMath::Sin(forty), UINT32_C(0x3f3ebfbd));
	passed &= ExpectBits("WWMath::SinTrig", WWMath::SinTrig(forty), UINT32_C(0x3f3ebfbd));
	passed &= ExpectBits("WWMath::Cos", WWMath::Cos(eightyOne), UINT32_C(0x3f46d4e5));
	passed &= ExpectBits("WWMath::CosTrig", WWMath::CosTrig(eightyOne), UINT32_C(0x3f46d4e5));
	passed &= ExpectBits("WWMath::Sqrt", WWMath::Sqrt(two), UINT32_C(0x3fb504f3));
	passed &= ExpectBits("WWMath::SqrtOrigin", WWMath::SqrtOrigin(two), UINT32_C(0x3fb504f3));
	passed &= ExpectBits("WWMath::Inv_Sqrt", WWMath::Inv_Sqrt(two), UINT32_C(0x3f3504f3));
	passed &= ExpectBits("WWMath::Atan2", WWMath::Atan2(positiveZero, negativeOne), UINT32_C(0x40490fdb));
	passed &= ExpectBits(
		"WWMath::Atan2Origin",
		WWMath::Atan2Origin(positiveZero, negativeOne),
		UINT32_C(0x40490fdb));
	passed &= ExpectBits(
		"WWMath::PowOrigin",
		WWMath::PowOrigin(two, negativeOnePointEight),
		UINT32_C(0x3e93088c));
	passed &= ExpectBits("WWMath::Acos", WWMath::Acos(negativeOne), UINT32_C(0x40490fdb));
	passed &= ExpectBits("WWMath::ACosTrig", WWMath::ACosTrig(negativeOne), UINT32_C(0x40490fdb));
	passed &= ExpectBits("WWMath::Asin", WWMath::Asin(positiveOne), UINT32_C(0x3fc90fdb));
	passed &= ExpectBits("WWMath::ASinTrig", WWMath::ASinTrig(positiveOne), UINT32_C(0x3fc90fdb));
	const TPoint2D<int> point2D(3, 4);
	const TPoint3D<int> point3D(2, 3, 6);
	if (point2D.Length() != 5 || point3D.Length() != 7) {
		std::cerr << "FAIL: integral Point length did not dispatch through the unambiguous deterministic wrapper\n";
		passed = false;
	}

	return passed ? 0 : 1;
}
