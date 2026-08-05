import {Button} from "@heroui/react/button";
import {Description} from "@heroui/react/description";
import {Input} from "@heroui/react/input";
import {Label} from "@heroui/react/label";
import {TextField} from "@heroui/react/textfield";

interface PathFieldProps {
  label: string;
  description: string;
  value: string;
  placeholder?: string;
  isDisabled?: boolean;
  onChange: (value: string) => void;
  onBrowse: () => void;
}

export function PathField({
  label,
  description,
  value,
  placeholder,
  isDisabled = false,
  onChange,
  onBrowse,
}: PathFieldProps) {
  return (
    <div className="flex items-end gap-3">
      <TextField className="min-w-0 flex-1" isDisabled={isDisabled}>
        <Label>{label}</Label>
        <Input
          placeholder={placeholder}
          value={value}
          variant="secondary"
          onChange={(event) => onChange(event.target.value)}
        />
        <Description>{description}</Description>
      </TextField>
      <Button
        aria-label={`Browse for ${label.toLowerCase()}`}
        className="mb-6 shrink-0"
        isDisabled={isDisabled}
        variant="outline"
        onPress={onBrowse}
      >
        Browse
      </Button>
    </div>
  );
}
