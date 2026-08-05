import {RadioButtonGroup} from "@heroui-pro/react/radio-button-group";
import {Card} from "@heroui/react/card";
import {Chip} from "@heroui/react/chip";
import {Description} from "@heroui/react/description";
import {Label} from "@heroui/react/label";

import type {BuildRequestUpdater, BuildTarget} from "../types";

interface TargetStepProps {
  hostOS: string;
  hostArch: string;
  target: BuildTarget;
  onUpdate: BuildRequestUpdater;
}

const targets: Array<{
  value: BuildTarget;
  title: string;
  description: string;
  architecture: string;
}> = [
  {
    value: "auto",
    title: "Current host",
    description: "Choose the native target that matches this computer.",
    architecture: "Recommended",
  },
  {
    value: "macos",
    title: "macOS",
    description: "Native Apple Silicon client with SDL3, DXVK, and MoltenVK.",
    architecture: "arm64",
  },
  {
    value: "linux",
    title: "Linux",
    description: "Native Vulkan client for a modern 64-bit Linux runtime.",
    architecture: "x86-64",
  },
  {
    value: "windows",
    title: "Windows",
    description: "Exploratory native DirectX 8 build for supported Windows hosts.",
    architecture: "x86",
  },
];

export function TargetStep({hostOS, hostArch, target, onUpdate}: TargetStepProps) {
  const supportedTargets = new Set<BuildTarget>(
    hostOS === "darwin" && hostArch === "arm64"
      ? ["auto", "macos", "linux"]
      : hostOS === "windows"
        ? ["auto", "windows"]
        : ["auto", "linux"],
  );

  return (
    <Card className="w-full">
      <Card.Header>
        <Card.Title>Choose a Build Target</Card.Title>
        <Card.Description>
          Native builds are the reliable path. The automatic choice resolves to {hostOS || "this host"}/{hostArch || "native"}.
        </Card.Description>
      </Card.Header>
      <Card.Content>
        <RadioButtonGroup
          aria-label="Build target"
          className="grid-cols-1 sm:grid-cols-2"
          layout="grid"
          name="target"
          value={target}
          variant="secondary"
          onChange={(value) => onUpdate("target", value as BuildTarget)}
        >
          {targets.filter((option) => supportedTargets.has(option.value)).map((option) => (
            <RadioButtonGroup.Item key={option.value} value={option.value}>
              <RadioButtonGroup.Indicator />
              <RadioButtonGroup.ItemContent>
                <div className="flex items-center gap-2 pr-7">
                  <Label>{option.title}</Label>
                  <Chip color={option.value === "auto" ? "accent" : "default"} size="sm" variant="soft">
                    <Chip.Label>{option.architecture}</Chip.Label>
                  </Chip>
                </div>
                <Description className="mt-1">{option.description}</Description>
              </RadioButtonGroup.ItemContent>
            </RadioButtonGroup.Item>
          ))}
        </RadioButtonGroup>
      </Card.Content>
    </Card>
  );
}
