import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from 'react';
import * as DialogPrimitive from '@radix-ui/react-dialog';
import * as DropdownPrimitive from '@radix-ui/react-dropdown-menu';
import * as TooltipPrimitive from '@radix-ui/react-tooltip';
import { cva, type VariantProps } from 'class-variance-authority';
import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

export const cn = (...values: ClassValue[]) => twMerge(clsx(values));

const buttonVariants = cva('button', {
  variants: {
    variant: { ghost: 'button-ghost', outline: 'button-outline', primary: 'button-primary' },
    size: { default: '', icon: 'button-icon' },
  },
  defaultVariants: { variant: 'ghost', size: 'default' },
});

export const Button = forwardRef<HTMLButtonElement, ButtonHTMLAttributes<HTMLButtonElement> & VariantProps<typeof buttonVariants>>(
  ({ className, variant, size, type = 'button', ...props }, ref) => (
    <button ref={ref} type={type} className={cn(buttonVariants({ variant, size }), className)} {...props} />
  ),
);
Button.displayName = 'Button';

export const Dialog = DialogPrimitive.Root;
export const DialogTrigger = DialogPrimitive.Trigger;
export const DialogClose = DialogPrimitive.Close;
export const DialogTitle = DialogPrimitive.Title;
export const DialogDescription = DialogPrimitive.Description;

export function DialogContent({ children, className, onCloseAutoFocus }: {
  children: ReactNode;
  className?: string;
  onCloseAutoFocus?: (event: Event) => void;
}) {
  return (
    <DialogPrimitive.Portal>
      <DialogPrimitive.Overlay className="dialog-overlay" />
      <DialogPrimitive.Content className={cn('dialog-content', className)} onCloseAutoFocus={onCloseAutoFocus}>
        {children}
      </DialogPrimitive.Content>
    </DialogPrimitive.Portal>
  );
}

export function Choice({ label, value, options, onChange, disabled = false }: {
  label: string;
  value: string;
  options: string[];
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  return (
    <DropdownPrimitive.Root>
      <DropdownPrimitive.Trigger asChild>
        <Button className="choice" aria-label={`${label}：${value}`} disabled={disabled}>
          <span className="choice-label">{label}</span>
          <span className="choice-value">{value}</span>
          <span aria-hidden="true">⌄</span>
        </Button>
      </DropdownPrimitive.Trigger>
      <DropdownPrimitive.Portal>
        <DropdownPrimitive.Content className="dropdown-content" sideOffset={6} align="start">
          <div className="dropdown-label">{label} · 本地预览</div>
          <DropdownPrimitive.RadioGroup value={value} onValueChange={onChange}>
            {options.map(option => (
              <DropdownPrimitive.RadioItem className="dropdown-item" key={option} value={option}>
                <span>{option}</span>
                <DropdownPrimitive.ItemIndicator>✓</DropdownPrimitive.ItemIndicator>
              </DropdownPrimitive.RadioItem>
            ))}
          </DropdownPrimitive.RadioGroup>
        </DropdownPrimitive.Content>
      </DropdownPrimitive.Portal>
    </DropdownPrimitive.Root>
  );
}

export function Hint({ text, children }: { text: string; children: ReactNode }) {
  return (
    <TooltipPrimitive.Provider delayDuration={350}>
      <TooltipPrimitive.Root>
        <TooltipPrimitive.Trigger asChild>{children}</TooltipPrimitive.Trigger>
        <TooltipPrimitive.Portal>
          <TooltipPrimitive.Content className="tooltip" sideOffset={6}>{text}</TooltipPrimitive.Content>
        </TooltipPrimitive.Portal>
      </TooltipPrimitive.Root>
    </TooltipPrimitive.Provider>
  );
}

export function PulsarMark({ size = 26 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 28 28" fill="none" aria-hidden="true">
      <path d="M4 8.2 13.8 2l4.1 2.3-9.8 6.2L4 8.2Z" fill="currentColor" opacity=".9" />
      <path d="m10.1 17.4 9.8-6.2L24 13.5l-9.8 6.2-4.1-2.3Z" fill="currentColor" opacity=".55" />
      <path d="m4 14.5 9.8 6.2 4.1-2.3 4-2.4v5.1l-8 4.9L4 20.3v-5.8Z" fill="currentColor" opacity=".78" />
    </svg>
  );
}
