import {Description} from "@heroui/react/description";
import {Label} from "@heroui/react/label";
import {Switch} from "@heroui/react/switch";

interface SwitchSettingProps {
  label: string;
  description: string;
  isSelected: boolean;
  isDisabled?: boolean;
  onChange: (isSelected: boolean) => void;
}
export function SwitchSetting({
  label,
  description,
  isSelected,
  isDisabled = false,
  onChange,
}: SwitchSettingProps) {
  return (
    <Switch
      className="w-full"
      isDisabled={isDisabled}
      isSelected={isSelected}
      onChange={onChange}
    >
      <Switch.Content className="flex w-full items-center justify-between gap-5">
        <div className="flex min-w-0 flex-col gap-0.5">
          <Label>{label}</Label>
          <Description>{description}</Description>
        </div>
        <Switch.Control className="shrink-0">
          <Switch.Thumb />
        </Switch.Control>
      </Switch.Content>
    </Switch>
  );
}
