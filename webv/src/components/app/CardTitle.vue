<template>
  <div class="flex items-center justify-between gap-3">
    <h2 class="text-[15px] font-semibold"><slot /></h2>
    <slot name="action" />
  </div>
</template>
