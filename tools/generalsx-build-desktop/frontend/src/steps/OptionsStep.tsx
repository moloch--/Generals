import {Button} from "@heroui/react/button";
import {Card} from "@heroui/react/card";
import {Description} from "@heroui/react/description";
import {Disclosure} from "@heroui/react/disclosure";
import {Input} from "@heroui/react/input";
import {Label} from "@heroui/react/label";
import {TextField} from "@heroui/react/textfield";

import {PathField} from "../components/PathField";
import {SwitchSetting} from "../components/SwitchSetting";
import type {BuildRequest, BuildRequestUpdater, DirectoryKind} from "../types";

interface OptionsStepProps {
  request: BuildRequest;
  onUpdate: BuildRequestUpdater;
  onBrowse: (kind: DirectoryKind) => void;
}

export function OptionsStep({request, onUpdate, onBrowse}: OptionsStepProps) {
  return (
    <Card className="w-full">
      <Card.Header>
        <Card.Title>Configure the Build</Card.Title>
        <Card.Description>
          Defaults favor a complete native client build. Open advanced settings only when you need to reuse local work.
        </Card.Description>
      </Card.Header>
      <Card.Content className="space-y-6">
        <div className="grid gap-x-8 gap-y-5 lg:grid-cols-2">
          <SwitchSetting
            description="Install missing host tools when required; approval prompts open in a native terminal."
            isSelected={request.installDeps}
            label="Install missing dependencies"
            onChange={(selected) => onUpdate("installDeps", selected)}
          />
          <SwitchSetting
            description="Include a target-native, loopback Online service sidecar."
            isSelected={request.withOnlineServer}
            label="Bundle the optional Online server"
            onChange={(selected) => onUpdate("withOnlineServer", selected)}
          />
          <SwitchSetting
            description="Print planned external actions without changing this computer or invoking SteamCMD."
            isSelected={request.dryRun}
            label="Dry run"
            onChange={(selected) => onUpdate("dryRun", selected)}
          />
          <SwitchSetting
            description="Required before automatic installation of applicable platform SDKs."
            isSelected={request.acceptSDKLicenses}
            label="Accept required SDK and tool licenses"
            onChange={(selected) => onUpdate("acceptSDKLicenses", selected)}
          />
        </div>

        <TextField className="max-w-xl">
          <Label>Default Online endpoint</Label>
          <Input
            placeholder={request.withOnlineServer ? "127.0.0.1:29900" : "tls://online.example.net:29900"}
            value={request.onlineEndpoint}
            variant="secondary"
            onChange={(event) => onUpdate("onlineEndpoint", event.target.value)}
          />
          <Description>
            Optional DNS name or IPv4 address, optional port, and optional lowercase tls:// prefix.
          </Description>
        </TextField>

        <Disclosure>
          <Disclosure.Heading>
            <Button className="w-full justify-between" slot="trigger" variant="ghost">
              Advanced settings
              <Disclosure.Indicator />
            </Button>
          </Disclosure.Heading>
          <Disclosure.Content>
            <Disclosure.Body className="space-y-6 pt-5">
              <div className="grid gap-5 lg:grid-cols-2">
                <PathField
                  description="Private dependency and download cache."
                  label="Builder cache"
                  value={request.cacheDir}
                  onBrowse={() => onBrowse("cacheDir")}
                  onChange={(value) => onUpdate("cacheDir", value)}
                />
                <PathField
                  description="Private SteamCMD installation managed by the builder."
                  label="SteamCMD directory"
                  value={request.steamCMDDir}
                  onBrowse={() => onBrowse("steamCMDDir")}
                  onChange={(value) => onUpdate("steamCMDDir", value)}
                />
              </div>

              <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_12rem]">
                <TextField>
                  <Label>Source repository</Label>
                  <Input
                    value={request.sourceRepo}
                    variant="secondary"
                    onChange={(event) => onUpdate("sourceRepo", event.target.value)}
                  />
                  <Description>Used only when the source destination does not exist.</Description>
                </TextField>
                <TextField>
                  <Label>Source ref</Label>
                  <Input
                    value={request.sourceRef}
                    variant="secondary"
                    onChange={(event) => onUpdate("sourceRef", event.target.value)}
                  />
                  <Description>Branch, tag, or commit.</Description>
                </TextField>
              </div>

              {request.withOnlineServer ? (
                <div className="space-y-5">
                  <PathField
                    description="Optional existing checkout; leave empty to use the managed clone."
                    label="Online server source"
                    value={request.onlineServerSource}
                    onBrowse={() => onBrowse("onlineServerSource")}
                    onChange={(value) => onUpdate("onlineServerSource", value)}
                  />
                  <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_12rem]">
                    <TextField>
                      <Label>Online server repository</Label>
                      <Input
                        value={request.onlineServerRepo}
                        variant="secondary"
                        onChange={(event) => onUpdate("onlineServerRepo", event.target.value)}
                      />
                    </TextField>
                    <TextField>
                      <Label>Server ref</Label>
                      <Input
                        value={request.onlineServerRef}
                        variant="secondary"
                        onChange={(event) => onUpdate("onlineServerRef", event.target.value)}
                      />
                    </TextField>
                  </div>
                </div>
              ) : null}

              <div className="grid gap-x-8 gap-y-5 lg:grid-cols-2">
                <SwitchSetting
                  description="Reuse the current native game build instead of recompiling."
                  isSelected={request.skipGameBuild}
                  label="Reuse existing game build"
                  onChange={(selected) => onUpdate("skipGameBuild", selected)}
                />
                <SwitchSetting
                  description="Fail instead of waiting for Steam Guard or installer interaction."
                  isSelected={request.nonInteractive}
                  label="Non-interactive mode"
                  onChange={(selected) => onUpdate("nonInteractive", selected)}
                />
                {request.target === "windows" ? (
                  <SwitchSetting
                    description="Keep the temporary Windows runtime stage for diagnosis."
                    isSelected={request.keepWindowsStage}
                    label="Keep Windows staging directory"
                    onChange={(selected) => onUpdate("keepWindowsStage", selected)}
                  />
                ) : null}
              </div>
            </Disclosure.Body>
          </Disclosure.Content>
        </Disclosure>
      </Card.Content>
    </Card>
  );
}
