import {Chip} from "@heroui/react/chip";

import appIconURL from "../../../../../assets/generalsx-zh_icon.png";

interface AppHeaderProps {
  hostOS: string;
  hostArch: string;
  isMock: boolean;
}

function hostLabel(hostOS: string, hostArch: string): string {
  const operatingSystem = hostOS === "darwin" ? "macOS" : hostOS.charAt(0).toUpperCase() + hostOS.slice(1);
  return [operatingSystem, hostArch].filter(Boolean).join(" · ");
}

export function AppHeader({hostOS, hostArch, isMock}: AppHeaderProps) {
  return (
    <header className="border-b border-separator bg-background/85 backdrop-blur-xl">
      <div className="mx-auto flex max-w-6xl items-center justify-between gap-6 px-5 py-5 sm:px-8">
        <div className="flex min-w-0 items-center gap-3.5">
          <img
            alt="GeneralsX Zero Hour"
            className="size-11 shrink-0 rounded-2xl object-cover"
            src={appIconURL}
          />
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="truncate text-lg font-semibold tracking-tight">Automated Build Tool</h1>
              {isMock ? (
                <Chip color="warning" size="sm" variant="soft">
                  <Chip.Label>Preview data</Chip.Label>
                </Chip>
              ) : null}
            </div>
            <p className="mt-0.5 text-sm text-muted">Build a verified GeneralsXZH artifact from your owned game files.</p>
          </div>
        </div>
        {hostOS ? (
          <Chip className="hidden shrink-0 sm:inline-flex" color="default" variant="soft">
            <Chip.Label>{hostLabel(hostOS, hostArch)}</Chip.Label>
          </Chip>
        ) : null}
      </div>
    </header>
  );
}
