<template>
  <div class="flex min-h-9 items-center justify-between gap-3 pb-2">
    <h3 class="font-medium"><slot /></h3>
    <slot name="action" />
  </div>
</template>
