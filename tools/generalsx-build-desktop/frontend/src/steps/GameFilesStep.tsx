import {RadioButtonGroup} from "@heroui-pro/react/radio-button-group";
import {Alert} from "@heroui/react/alert";
import {Card} from "@heroui/react/card";
import {Description} from "@heroui/react/description";
import {Input} from "@heroui/react/input";
import {Label} from "@heroui/react/label";
import {TextField} from "@heroui/react/textfield";

import {PathField} from "../components/PathField";
import type {BuildRequest, BuildRequestUpdater, DirectoryKind} from "../types";

interface GameFilesStepProps {
  hostOS: string;
  request: BuildRequest;
  onUpdate: BuildRequestUpdater;
  onBrowse: (kind: DirectoryKind) => void;
}

export function GameFilesStep({hostOS, request, onUpdate, onBrowse}: GameFilesStepProps) {
  return (
    <Card className="w-full">
      <Card.Header>
        <Card.Title>Locate Source and Owned Game Files</Card.Title>
        <Card.Description>
          The builder keeps source, caches, retail data, and generated output in separate locations.
        </Card.Description>
      </Card.Header>
      <Card.Content className="space-y-6">
        <div className="grid gap-5 lg:grid-cols-2">
          <PathField
            description="An existing checkout or a private destination for the automatic clone."
            label="GeneralsX source"
            placeholder="~/GeneralsX/source"
            value={request.repoRoot}
            onBrowse={() => onBrowse("repoRoot")}
            onChange={(value) => onUpdate("repoRoot", value)}
          />
          <PathField
            description="A directory containing retail Zero Hour data that you legally own."
            label="Zero Hour retail data"
            placeholder="~/GeneralsX/GeneralsZH"
            value={request.assetsDir}
            onBrowse={() => onBrowse("assetsDir")}
            onChange={(value) => onUpdate("assetsDir", value)}
          />
          <PathField
            description="The self-extracting file is written here after verification."
            label="SFX output"
            placeholder="~/GeneralsX/source/build/sfx/GeneralsXZH-sfx"
            value={request.output}
            onBrowse={() => onBrowse("output")}
            onChange={(value) => onUpdate("output", value)}
          />
          {request.target === "macos" || (request.target === "auto" && hostOS === "darwin") ? (
            <PathField
              description="macOS also receives a Finder-launchable application bundle."
              label="macOS app output"
              placeholder="~/GeneralsX/source/build/sfx/GeneralsXZH.app"
              value={request.appOutput}
              onBrowse={() => onBrowse("appOutput")}
              onChange={(value) => onUpdate("appOutput", value)}
            />
          ) : null}
        </div>

        <RadioButtonGroup
          className="grid-cols-1 sm:grid-cols-2"
          layout="grid"
          name="retail-acquisition"
          value={request.skipAssets ? "existing" : "steamcmd"}
          variant="secondary"
          onChange={(value) => onUpdate("skipAssets", value === "existing")}
        >
          <Label className="col-span-full">Retail file source</Label>
          <RadioButtonGroup.Item value="steamcmd">
            <RadioButtonGroup.Indicator />
            <RadioButtonGroup.ItemContent>
              <Label>Download or repair with SteamCMD</Label>
              <Description>Open a private SteamCMD prompt only when files need acquisition or repair.</Description>
            </RadioButtonGroup.ItemContent>
          </RadioButtonGroup.Item>
          <RadioButtonGroup.Item value="existing">
            <RadioButtonGroup.Indicator />
            <RadioButtonGroup.ItemContent>
              <Label>Use existing files only</Label>
              <Description>Validate the selected complete retail tree and keep the workflow in the app.</Description>
            </RadioButtonGroup.ItemContent>
          </RadioButtonGroup.Item>
        </RadioButtonGroup>

        {!request.skipAssets ? (
          <>
            <Alert status="accent">
              <Alert.Indicator />
              <Alert.Content>
                <Alert.Title>SteamCMD authentication stays in a real terminal</Alert.Title>
                <Alert.Description>
                  SteamCMD privately prompts there for your password and Steam Guard code. When acquisition finishes, the structured build resumes in this app. The GUI never requests, receives, or stores either secret.
                </Alert.Description>
              </Alert.Content>
            </Alert>

            <TextField className="max-w-xl">
              <Label>Steam account name</Label>
              <Input
                autoComplete="username"
                placeholder="Account name (not an email or password)"
                value={request.steamUser}
                variant="secondary"
                onChange={(event) => onUpdate("steamUser", event.target.value)}
              />
              <Description>Password and Guard fields intentionally do not exist.</Description>
            </TextField>
          </>
        ) : null}
      </Card.Content>
    </Card>
  );
}
