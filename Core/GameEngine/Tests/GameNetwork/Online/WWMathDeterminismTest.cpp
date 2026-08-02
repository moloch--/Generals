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
#include "WWMath/sphere.h"
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
static_assert(sizeof(double) == sizeof(std::uint64_t));
static_assert(std::numeric_limits<float>::is_iec559);
static_assert(std::numeric_limits<double>::is_iec559);
static_assert(std::is_same_v<decltype(WWMath::SqrtOrigin(1.0f)), float>);
static_assert(std::is_same_v<decltype(WWMath::SqrtOrigin(1.0)), double>);

float FloatFromBits(std::uint32_t bits)
{
	float value = 0.0f;
	std::memcpy(&value, &bits, sizeof(value));
	return value;
}

double DoubleFromBits(std::uint64_t bits)
{
	double value = 0.0;
	std::memcpy(&value, &bits, sizeof(value));
	return value;
}

std::uint32_t FloatBits(float value)
{
	std::uint32_t bits = 0;
	std::memcpy(&bits, &value, sizeof(bits));
	return bits;
}

std::uint64_t DoubleBits(double value)
{
	std::uint64_t bits = 0;
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

bool ExpectBits(const char *name, double actual, std::uint64_t expected)
{
	const std::uint64_t actualBits = DoubleBits(actual);
	if (actualBits == expected)
		return true;

	std::cerr << "FAIL: " << name << " expected 0x"
		<< std::hex << std::setfill('0') << std::setw(16) << expected
		<< ", got 0x" << std::setw(16) << actualBits << '\n';
	return false;
}

} // namespace

// GeneralsX @bugfix OpenAI 02/08/2026 Detect CRT, x87, or architecture-specific fallback in simulation math.
int main()
{
	const float forty = FloatFromBits(UINT32_C(0x42200000));
	const float eightyOne = FloatFromBits(UINT32_C(0x42a20000));
	const float two = FloatFromBits(UINT32_C(0x40000000));
	const float positiveZero = FloatFromBits(UINT32_C(0x00000000));
	const float negativeOne = FloatFromBits(UINT32_C(0xbf800000));
	const float negativeOnePointEight = FloatFromBits(UINT32_C(0xbfe66666));
	const float positiveOne = FloatFromBits(UINT32_C(0x3f800000));
	const double doubleTwo = DoubleFromBits(UINT64_C(0x4000000000000000));
	const double doublePositiveZero = DoubleFromBits(UINT64_C(0x0000000000000000));
	const double doubleNegativeOne = DoubleFromBits(UINT64_C(0xbff0000000000000));
	const double doubleNegativeOnePointEight = DoubleFromBits(UINT64_C(0xbffccccccccccccd));

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
	passed &= ExpectBits(
		"WWMath::SqrtOrigin(double)",
		WWMath::SqrtOrigin(doubleTwo),
		UINT64_C(0x3ff6a09e667f3bcd));
	passed &= ExpectBits(
		"WWMath::Atan2Origin(double)",
		WWMath::Atan2Origin(doublePositiveZero, doubleNegativeOne),
		UINT64_C(0x400921fb54442d18));
	passed &= ExpectBits(
		"WWMath::PowOrigin(double)",
		WWMath::PowOrigin(doubleTwo, doubleNegativeOnePointEight),
		UINT64_C(0x3fd2611186bae674));
	passed &= ExpectBits(
		"WWMath::Div_FixNaN(double)",
		WWMath::Div_FixNaN(1.0, 10.0),
		UINT64_C(0x3fb999999999999a));
	passed &= ExpectBits(
		"WWMath::Div_FixNaN(double fallback)",
		WWMath::Div_FixNaN(1.0, 0.0, -7.25),
		UINT64_C(0xc01d000000000000));
	passed &= ExpectBits(
		"WWMath::Pairwise_Multiply_Add_4",
		WWMath::Pairwise_Multiply_Add_4(
			FloatFromBits(UINT32_C(0x450ee190)), FloatFromBits(UINT32_C(0xbebcb0cf)),
			FloatFromBits(UINT32_C(0x453facb5)), FloatFromBits(UINT32_C(0xbe81c129)),
			FloatFromBits(UINT32_C(0xc5886979)), FloatFromBits(UINT32_C(0x3fa6b65e)),
			FloatFromBits(UINT32_C(0xc593aed0)), FloatFromBits(UINT32_C(0x3ef8bcd5))),
		UINT32_C(0xc6160404));
	const double radiusCellSize = 50.0;
	const double radiusDx = 315.0 * radiusCellSize;
	const double radiusDy = 1972.0 * radiusCellSize;
	passed &= ExpectBits(
		"Partition radius-vector double boundary",
		WWMath::SqrtOrigin(radiusDx * radiusDx + radiusDy * radiusDy) / radiusCellSize,
		UINT64_C(0x409f340000000000));
	const Vector3 sphereVertices[] = {
		{ FloatFromBits(UINT32_C(0x45f2d800)), FloatFromBits(UINT32_C(0x45fe7000)), FloatFromBits(UINT32_C(0xc5dcf800)) },
		{ FloatFromBits(UINT32_C(0xc563c000)), FloatFromBits(UINT32_C(0x45a42000)), FloatFromBits(UINT32_C(0xc5fd4800)) },
		{ FloatFromBits(UINT32_C(0x45b6f800)), FloatFromBits(UINT32_C(0x45854000)), FloatFromBits(UINT32_C(0x4506f000)) },
	};
	const SphereClass sphere(sphereVertices, 3);
	passed &= ExpectBits("SphereClass::Center.X", sphere.Center.X, UINT32_C(0x44d4387a));
	passed &= ExpectBits("SphereClass::Center.Y", sphere.Center.Y, UINT32_C(0x459e157e));
	passed &= ExpectBits("SphereClass::Center.Z", sphere.Center.Z, UINT32_C(0xc5509210));
	passed &= ExpectBits("SphereClass::Radius", sphere.Radius, UINT32_C(0x45f2bb5a));
	const TPoint2D<int> point2D(3, 4);
	const TPoint3D<int> point3D(2, 3, 6);
	const TPoint2D<int> precisionPoint(4141, 91);
	if (point2D.Length() != 5 || point3D.Length() != 7 || precisionPoint.Length() != 4141) {
		std::cerr << "FAIL: integral Point length did not dispatch through the unambiguous deterministic wrapper\n";
		passed = false;
	}

	return passed ? 0 : 1;
}
