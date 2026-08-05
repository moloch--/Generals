import {Alert} from "@heroui/react/alert";
import {Card} from "@heroui/react/card";
import {Chip} from "@heroui/react/chip";

import {SwitchSetting} from "../components/SwitchSetting";
import {targetLabel} from "../lib/request";
import type {BuildRequest, ValidationIssue} from "../types";

interface ReviewStepProps {
  request: BuildRequest;
  legalAcknowledged: boolean;
  issues: ValidationIssue[];
  onLegalAcknowledgedChange: (acknowledged: boolean) => void;
}

function enabledOptions(request: BuildRequest): string[] {
  const options = [
    request.installDeps ? "Install dependencies" : "Existing toolchain",
    request.skipAssets ? "Existing retail files" : "SteamCMD acquisition",
    request.withOnlineServer ? "Bundled Online server" : "Client only",
    request.dryRun ? "Dry run" : "Create artifact",
  ];
  if (request.onlineEndpoint) {
    options.push("Custom Online endpoint");
  }
  if (request.skipGameBuild) {
    options.push("Reuse game build");
  }
  return options;
}

export function ReviewStep({
  request,
  legalAcknowledged,
  issues,
  onLegalAcknowledgedChange,
}: ReviewStepProps) {
  const errors = issues.filter((issue) => issue.severity !== "warning");
  const warnings = issues.filter((issue) => issue.severity === "warning");

  return (
    <div className="space-y-6">
      <Card className="w-full">
        <Card.Header>
          <Card.Title>Review the Build Plan</Card.Title>
          <Card.Description>Paths remain local. No retail data or Steam secret is sent to GeneralsX.</Card.Description>
        </Card.Header>
        <Card.Content className="space-y-6">
          <dl className="grid gap-x-8 gap-y-5 sm:grid-cols-2">
            <div>
              <dt className="text-xs font-medium text-muted">Target</dt>
              <dd className="mt-1 text-sm font-medium">{targetLabel(request.target)}</dd>
            </div>
            <div>
              <dt className="text-xs font-medium text-muted">Source</dt>
              <dd className="mt-1 break-all text-sm">{request.repoRoot}</dd>
            </div>
            <div>
              <dt className="text-xs font-medium text-muted">Owned retail data</dt>
              <dd className="mt-1 break-all text-sm">{request.assetsDir}</dd>
            </div>
            <div>
              <dt className="text-xs font-medium text-muted">SFX output</dt>
              <dd className="mt-1 break-all text-sm">{request.output}</dd>
            </div>
          </dl>

          <div className="flex flex-wrap gap-2">
            {enabledOptions(request).map((option) => (
              <Chip key={option} color="default" variant="soft">
                <Chip.Label>{option}</Chip.Label>
              </Chip>
            ))}
          </div>

          {request.dryRun ? (
            <Alert status="accent">
              <Alert.Indicator />
              <Alert.Content>
                <Alert.Title>Dry runs stay in the app</Alert.Title>
                <Alert.Description>
                  The builder will validate and print planned external actions without invoking SteamCMD, opening a terminal, or changing this computer.
                </Alert.Description>
              </Alert.Content>
            </Alert>
          ) : request.skipAssets ? (
            <Alert status="accent">
              <Alert.Indicator />
              <Alert.Content>
                <Alert.Title>No Steam sign-in is required</Alert.Title>
                <Alert.Description>
                  The selected retail tree will be validated without requesting Steam credentials. If a missing host dependency needs administrator approval, its installer opens in a native terminal and then returns here.
                </Alert.Description>
              </Alert.Content>
            </Alert>
          ) : (
            <Alert status="accent">
              <Alert.Indicator />
              <Alert.Content>
                <Alert.Title>SteamCMD acquisition opens a real terminal</Alert.Title>
                <Alert.Description>
                  SteamCMD privately prompts there for password and Steam Guard. Missing host dependencies may also open a terminal for administrator approval. Each command returns to this build when it exits; the GUI never sees Steam secrets.
                </Alert.Description>
              </Alert.Content>
            </Alert>
          )}

          <Alert status="warning">
            <Alert.Indicator />
            <Alert.Content>
              <Alert.Title>The finished artifact contains copyrighted retail game data</Alert.Title>
              <Alert.Description>
                Keep it private and use it only with files you legally own. Do not commit, upload, publish, or redistribute the artifact. GeneralsX source is GPLv3 software provided without warranty; the source checkout includes the complete license and EA additional terms.
              </Alert.Description>
            </Alert.Content>
          </Alert>

          <div className="py-1">
            <SwitchSetting
              description="I own the selected retail files and will keep the generated artifact private."
              isSelected={legalAcknowledged}
              label="Confirm ownership and private use"
              onChange={onLegalAcknowledgedChange}
            />
          </div>

          {warnings.length > 0 ? (
            <Alert status="warning">
              <Alert.Indicator />
              <Alert.Content>
                <Alert.Title>Review these build warnings</Alert.Title>
                <Alert.Description>
                  <ul className="mt-2 list-inside list-disc space-y-1">
                    {warnings.map((issue) => <li key={`${issue.field}-${issue.message}`}>{issue.message}</li>)}
                  </ul>
                </Alert.Description>
              </Alert.Content>
            </Alert>
          ) : null}

          {errors.length > 0 ? (
            <Alert status="danger">
              <Alert.Indicator />
              <Alert.Content>
                <Alert.Title>Resolve these settings before building</Alert.Title>
                <Alert.Description>
                  <ul className="mt-2 list-inside list-disc space-y-1">
                    {errors.map((issue) => <li key={`${issue.field}-${issue.message}`}>{issue.message}</li>)}
                  </ul>
                </Alert.Description>
              </Alert.Content>
            </Alert>
          ) : null}
        </Card.Content>
      </Card>
    </div>
  );
}
