import { SelectMenu } from './SelectMenu'

export interface ProviderOption {
  id: string
  label: string
  available: boolean
}

export function ProviderSelect({
  value,
  options,
  disabled,
  placement = 'auto',
  onChange,
}: {
  value: string
  options: readonly ProviderOption[]
  disabled: boolean
  placement?: 'auto' | 'top' | 'bottom'
  onChange: (id: string) => void
}) {
  return (
    <div className="select-wrap catalog-select provider-select">
      <span>Провайдер</span>
      <SelectMenu
        ariaLabel="Провайдер"
        value={value}
        disabled={disabled}
        placement={placement}
        options={options.map(opt => ({
          value: opt.id,
          label: opt.label,
          disabled: !opt.available,
        }))}
        onChange={onChange}
      />
    </div>
  )
}
