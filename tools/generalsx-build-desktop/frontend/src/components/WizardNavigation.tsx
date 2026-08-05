import {Stepper} from "@heroui-pro/react/stepper";

import {wizardSteps} from "../types";

interface WizardNavigationProps {
  currentStep: number;
  onStepChange: (step: number) => void;
}
export function WizardNavigation({currentStep, onStepChange}: WizardNavigationProps) {
  return (
    <div className="overflow-x-auto px-1 pb-1">
      <Stepper
        aria-label="Build configuration steps"
        className="min-w-[640px]"
        currentStep={currentStep}
        onStepChange={onStepChange}
      >
        {wizardSteps.map((step) => (
          <Stepper.Step key={step.title}>
            <Stepper.Indicator />
            <Stepper.Content>
              <Stepper.Title>{step.title}</Stepper.Title>
              <Stepper.Description>{step.description}</Stepper.Description>
            </Stepper.Content>
            <Stepper.Separator />
          </Stepper.Step>
        ))}
      </Stepper>
    </div>
  );
}
